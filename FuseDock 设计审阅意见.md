# FuseDock 设计审阅意见

Oct 26, 2026 · @XueHaonan

## 总体判断

这份设计可以继续投入。审阅对象是《FuseDock 设计审阅总稿》（2026-10-24，代码快照 `620a358`）。文档本身质量很高：状态标记诚实、分层清楚、§18 把最关键的问题问对了。先身份、后版本、再缓存、最后写入，这个顺序是对的。

意见分三类：契约定义里不够精确或有漏洞的地方；几个文档没覆盖、但会在持久缓存落地之前就暴露的运行时问题；以及对 §16.3 十个待定项的具体立场。

最需要先处理的三件事：

- 修正 §9.2 不变量的作用域：ContentVersion 只在对象内有意义。
- 把读取绑定定义到"每个远端请求"，而不是一次 OpenRead。
- 补上 FSKit extension 重启后拿不到新 capability 的生命周期缺口。

## 做得对的地方

以下决定应该保留，不要在后续迭代中被稀释。

- 三个 namespace 分开、版本不进 NodeKey，和 NFSv4 把 filehandle 与 change attribute 分离是同一思路。
- §9.2 把 ContentVersion 定义为不透明强验证器、不要求可比较或递增，是正确的最小承诺。
- §4.2 第 7 条"缓存是优化，损坏或写失败不能让一次有效的远端读取失败"作为不变量写出来，很多项目做不到。
- 诚实地把 Baidu dlink 未绑定 revision 列出来，而不是先上缓存再说。

## 需要修正或补强的点

按重要性排列。前四条影响正确性，其余影响可用性与实现成本。

1. **§9.2 的不变量写错了一处。** 原文"ContentVersion 非空且相同 ⇒ ObjectID 相同"过强：两个不同对象可以合法地共享同一个 ContentVersion（MD5、S3 ETag、任何内容派生的 token，两个相同文件就会撞）。应把作用域收到对象内：`(ObjectID, ContentVersion) 相同 ⇒ Size 与字节相同`。否则 §9.5 的"摘要辅助"方案与主不变量冲突。
2. **读取绑定的粒度应定在"每个远端请求"，而不是 OpenRead。** 一个 handle 上的多次 ReadAt 通常是多个 HTTP 请求，签名 URL（PKU osdownload、Baidu dlink）还会过期续签。需要明确：ExactRevisionRead 指每次 ReadAt 发出的请求都绑定版本；续签必须绑定同一 rev，rev 已不存在即 ErrConflict。条件 GET 用 `If-Match` 而不是 `If-Range`：后者不匹配时返回 200 全文而非 412，必须把 200 当作冲突处理。每个响应都校验 206 + Content-Range + 响应 ETag。
3. **最危险的失效（同版本、不同内容）本地检测不到。** conformance 测试能发现"版本变了内容没变"（无害），发现不了反过来的情况。建议：
   - provider 有摘要时（Baidu MD5、Drive md5Checksum、S3 非分段 ETag）作为二级校验。whole-file ≤16 MiB 可以整文件算完摘要再入缓存，这样 Baidu 的小文件可以安全进入持久缓存，不必一刀切"暂不启用"，前提是 XPAN 的 md5 字段经实测可靠。
   - 把"读到一半由另一个 session 改写文件"做成最重要的集成测试：Local 用 os.Root 可自动化，PKU/Baidu 用真实账号跑可选 CI。
   - PKU 的 rev 还需验证：是否存在不碰 rev 的内容变更路径（服务端在线编辑、版本回滚等）。
4. **FSKit 的生命周期有一个 bootstrap 模型覆盖不了的情况。** extension 是系统按需拉起、可能被重启的进程；一次性 capability 被 claim 之后，extension 若重启就拿不到新的，除非 CLI 恰好在场。如果 capability 是通过 mount 资源 URL 或参数交给 extension 的，重启后同一参数已经失效。这是推进 UDS/XPC + peer code-signing 的硬理由（extension 可以随时重连、凭签名身份认证），不只是安全加固。建议把 daemon 和控制面（§15 的 B.5/B.6）提到 provider v2 之前。另外，如果数据面也走 JSON（base64）过 loopback，warm hit 的吞吐和 CPU 会被它封顶，数据面应该是二进制 body。
5. **2 秒元数据 TTL 对 API 型 provider 太激进。** Finder 会频繁 relist，每次 relist 对 Baidu 是一串分页请求，很快撞限频。只读架构下本地不产生变更，唯一的刷新理由是远端变更，用户对网络盘 30–60 秒的陈旧度是可接受的（NFS 属性缓存默认 3–60 秒，rclone dir-cache 默认 5 分钟）。建议 per-provider 可调、默认拉长，并给 CLI 一个显式 invalidate。
6. **macOS 上的批量读取风险没有讨论。** Spotlight/mdworker、QuickLook 缩略图、Finder 图标视图会对挂载卷做大面积读取，对云盘等于下载全部内容并把缓存 LRU 冲掉。需要在根目录合成 `.metadata_never_index`（并验证 FSKit 卷是否尊重它或有对应 capability），Linux 上留意 tracker/baloo，WebDAV 拒绝 `Depth: infinity`。同类问题：4 MiB 对齐块 + 1 块预读，对 QuickLook 和视频播放器"读头、读尾、再 seek"的模式读放大很大。建议先用 Finder/QuickLook/mpv 测一下再锁定块大小，或者让 fetch 粒度小于块粒度。
7. **内存没有总预算。** "每个 open handle 保留一个 4 MiB block"是无界的，Finder 生成缩略图时上千个 handle 就是几 GiB。有了持久缓存之后，per-handle 块应该变成全局有上限的内存 LRU，或者去掉。
8. **缓存实现细节。**
   - 每块 tmp + fsync + rename + 目录 fsync 在小文件 whole-file 场景很贵。缓存可丢弃、块有 payload hash，可以不 fsync：崩溃后 rename 可见但内容残缺的文件由校验兜底删除。
   - §10.6 的"必要时校验"要定死。建议每进程首次命中校验一次、内存索引记为已校验；否则"检测损坏"只是启动扫描时的承诺。每次命中都校验会对小粒度随机读造成放大。
   - `entries/<namespaceTag>/` 下再按 keyHash 前两位 fan-out，百万小文件时单目录会很慢。
   - LRU 顺序跨重启直接用文件 mtime（命中时 touch），不需要额外索引。
   - 打开块文件用 `openat` + `O_NOFOLLOW`，不要先 lstat 再 open（有竞态）。
9. **inode/FSItem 的建议可以更简单。** FUSE 不需要 hash：nodeid 顺序分配 + NodeKey↔nodeid map + FORGET 回收，st\_ino = nodeid；跨 remount 稳定的 st\_ino 不是 FUSE 的要求。FSKit 要真的 persistent object ID 就需要持久映射表，而 hash + 碰撞检测本身也要状态，所以别两头都要：要么现在把 `supportsPersistentObjectIDs` 改成 false（一行改动，不必等阶段 A），要么由 daemon 持有一张 NodeKey → 64 位序号的持久表。volume identifier 用 UUIDv5(AccountID, scope) 确定性派生，Finder 收藏才有稳定锚点；同一 account + scope 重复挂载直接拒绝。
10. **provider 接口 v2 的形态。** 现在 Stat/List/OpenRead 都接整个 Object，而 §8.7 已经指出客户端给的 Object 不可信。v2 应让 wire 协议只传 ID/NodeKey，daemon 持有 Object；provider 侧用 `Stat(ctx, ObjectID)`、`OpenRead(ctx, ObjectRevision)`。Go 里与其做平行的 v2 interface 加兼容窗口，不如用可选接口（`RevisionReader`、`Capabilities()`）让 VFS 类型断言：缺接口 ⇒ 弱 provider，正好是 §9.9 要的"不能从旧字段推断强版本"的安全默认，而且不用维护两套 SDK。顺带一提，凭据库 item key 改成 AccountID 之后，§7.1 的改名迁移问题自然消失。
11. **缺失的运行时主题，建议在持久缓存之前写清。**
    - 重试/退避归谁：provider 还是 VFS？两边都重试会出 retry storm。读是幂等的，建议 VFS 不重试，provider 内部做带 Retry-After 的退避。
    - 认证过期时挂载的行为：FSKit 不能弹窗。建议 daemon 暴露"需要重新登录"状态，surface 统一返回 EACCES，CLI/GUI 重新登录后不卸载直接恢复。
    - 错误映射表：provider error → VFS error → errno / HTTP status，每个 surface 一列。尤其 ErrConflict 在只读 handle 上映射成什么，ESTALE 比 EIO 语义更准。
    - 观测性：一个请求 ID 贯穿 extension → daemon → provider，从第一天就有比后面补便宜得多。
    - size 未知的对象（Drive 原生文档导出）：getattr 必须返回 size，这类对象不能伪装成普通文件，见待定项第 10 条。

## 对 §16.3 十个待定项的立场

十项都给出了明确倾向，编号与原文 §16.3 一致。

| # | 待定项 | 立场 |
| --- | --- | --- |
| 1 | 是否拆分 ContentVersion + MetadataVersion | 采纳。MetadataVersion 做成可选字段，provider 没有就由 VFS 派生弱指纹；契约核心只有 ContentVersion + 能力声明。 |
| 2 | 精确 revision 读取是否作硬门槛 | 硬门槛定义为"每个读请求绑定版本"的能力，不限定机制：revision 读，或 `If-Match` 条件 GET（强 ETag + 响应 ETag 校验 + 探测服务器对错误 If-Match 确实返回 412）。仅响应校验只对版本不可变的对象存储（S3/GCS/Azure）放行；Stat 前后不算。 |
| 3 | 能力属于 factory、session 还是对象 | session 为准：factory 声明上限，session 探测后收窄，对象级通过 Kind 或空 ContentVersion 退出。 |
| 4 | AccountID 是否包含 binding fingerprint | fingerprint 存在 ID 旁边做加载时校验，不参与派生 ID。覆盖的字段集要显式枚举并带版本号，否则升级时多一个无关 option 就会导致不必要轮换；轮换不只是冷缓存，还会让 Finder 收藏和持久 inode 表失效。 |
| 5 | FUSE/FSKit 的 64 位标识映射 | FUSE 顺序分配 nodeid，不 hash；FSKit 要么声明 false，要么由 daemon 持有显式持久表（见上文第 9 条）。 |
| 6 | daemon 是否成为唯一 cache owner | 是：单 daemon 单 owner，per-user launchd agent / systemd user service。多 daemon 共享缓存不要做，真需要就分 cache root。 |
| 7 | 启动扫描还是 bbolt/自定义索引 | 不需要 bbolt 做块索引：keyHash 就是文件名，目录本身就是索引。启动只需要 budget 和 LRU：后台扫描，扫完之前 budget 按保守值算。 |
| 8 | owner-only 明文缓存是否符合威胁模型 | 默认可接受，与浏览器缓存、OS page cache 同一保护级别，把这个假设写进威胁模型。提供 per-account 关闭持久缓存的开关；加密不要现在做，同用户攻击者面前 daemon 持有 key，加密不增加保护。 |
| 9 | 单账户与 aggregate view 的默认 UX | macOS 默认每账户一个 volume，Finder 的 eject 语义自然对应"断开账户"；aggregate root 给 CLI/WebDAV。别名和 Unicode 冲突直接拒绝，不做转义层。 |
| 10 | 特殊对象是否需要独立 provider subtypes | 用 Kind 显式建模（Document/Shortcut/Export 等）。Drive 原生文档默认隐藏或 0 字节占位，导出只在 CLI/应用 surface 提供，那里可以先导出再告知 size。 |

## 文档本身

三处补充即可，结构不用动。

- 缺一张跨进程的读路径时序图（kernel → extension → broker/daemon → VFS → cache → provider），以及缓存加入后坐在哪一层。
- 缺基线测量：PKU/Baidu 单请求延迟、range 吞吐、Finder 一次 relist 发多少 API 调用。块大小、TTL、预读这些默认值目前都是在没有数字的情况下定的。
- §0.1 和 §18 重复较多，可以合并。
