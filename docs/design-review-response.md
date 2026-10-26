# FuseDock 设计审阅响应

| 日期 | 2026-10-26 |
| --- | --- |
| 审阅输入 | [FuseDock 设计审阅意见](../FuseDock%20设计审阅意见.md) |
| 被审阅文档 | [FuseDock 设计审阅总稿](design-review.md) |

## 结论

审阅意见支持继续投入，同时发现了三处必须先修正的设计缺口：`ContentVersion` 的比较作用域、读取绑定的请求粒度，以及 FSKit extension 重启后的 capability 生命周期。我们接受这三项修正，并据此调整实施顺序。

新的优先级是：

1. 修正契约并加入立即生效的安全护栏。
2. 建立不可变账户身份和可验证的 provider binding。
3. 建立 daemon、可重连的认证控制面和二进制数据面。
4. 收紧 provider 的逐请求版本语义与 conformance。
5. 完成 NodeKey、FUSE/FSKit 标识和 volume 生命周期。
6. 设置元数据、内存和性能基线。
7. 最后才实现并默认启用持久块缓存。

multi-account、额外 provider、Windows 和写入仍排在只读基础设施稳定之后。

## 已采纳的修正

### ContentVersion 的作用域

原设计要求相同 `ContentVersion` 推出相同 `ObjectID`，这个方向错误。内容摘要天然允许两个不同对象拥有相同值。正式不变量改为：

```text
对同一个 AccountID + ProviderID + ObjectID：
(ContentVersion 非空且相同) => Size 相同且可读字节完全相同

同一对象的可读字节发生变化 => ContentVersion 必须变化
```

不同对象可以共享 ContentVersion。缓存键仍包含完整 NodeKey，因此共享版本不会让两个对象共享缓存身份。

### 读取绑定到每个远端请求

`ExactRevisionRead` 不再只描述一次 `OpenRead`。一个 Reader 的每次 `ReadAt` 可能产生多个 HTTP 请求、续签多个签名 URL，因此每个实际远端读请求都必须绑定同一个 revision。

- PKU 每次签名请求和对象 GET 都携带同一 `rev`；续签也必须使用该 rev。
- 条件 GET 使用 `If-Match`，不用 `If-Range` 作为版本门槛。
- 每次响应必须验证状态码、`Content-Range`、总大小和响应版本；任何不一致返回 `ErrConflict`。
- 服务端忽略 `If-Match`、返回 200 全文或返回另一个 ETag 都视为冲突，不能把响应当作请求版本。
- revision 已不存在时返回 `ErrConflict`，不能在 handle 内静默切换到最新版本。

### 摘要作为第二道校验

provider 报告完整文件摘要时，把它作为额外一致性检查，而不是强版本的替代品：

- 摘要必须带算法和覆盖范围；在 VFS 内部统一使用小写编码。
- MD5 只用于兼容供应商元数据和检测意外不一致，不作为抗攻击承诺。
- 小文件整文件下载后再计算摘要，只有与 metadata 摘要一致时才发布缓存。
- 该路径可以让经过实测的 Baidu 小文件有限缓存，但不能让大文件 range 或未知摘要对象获得同样保证。
- 摘要相同而内容不同仍然无法被本地发现；这必须由 provider 能力和 conformance 证据处理，不能伪装成可证明。

### FSKit 生命周期

一次性 bootstrap capability 不能覆盖 extension 被系统重启的情况。新的强制要求是：

- daemon 使用 launchd user service 运行。
- FSKit extension 通过 UDS/XPC 重连，并由 peer code-signing 身份认证。
- capability 可以短期、按 target 授权和轮换，但 extension 重启后能重新认证，不依赖 CLI 留在现场。
- 数据面使用原始二进制 body 或明确的二进制 frame；不要把文件内容放进 JSON/base64。

当前 broker 的 `/v1/read` 响应已经是 `application/octet-stream`，问题主要在生命周期、请求格式和未来 UDS 协议设计，而不是现有响应已经 base64。

## 已采纳的运行时调整

- 元数据 TTL 按 provider/session 配置。API 型 provider 不再固定 2 秒；先测 Finder relist 和 provider 限频，再确定远程默认值。
- 增加显式 metadata invalidation 命令；只读本地操作不应借 TTL 制造无谓远端请求。
- 对 macOS 合成 `.metadata_never_index`，并通过 Finder/Spotlight 实机验证；处理远端同名文件冲突。
- WebDAV 拒绝 `Depth: infinity`。
- 每个 handle 的 4 MiB 内存块改为全局有界内存预算；持久缓存完成后进一步移除重复 per-handle block。
- provider 内部负责网络重试和 `Retry-After`；VFS 不重复网络重试。冲突后的重新解析必须在返回任何字节前完成，且只能发生一次。
- daemon 暴露 `reauth_required`；挂载保持存在，surface 返回 `EACCES`，CLI/GUI 重新登录后恢复。
- 建立统一错误映射表。只读 handle 的 `ErrConflict` 映射为 `ESTALE`；写冲突仍映射为 `EBUSY`。
- 请求 ID 从 extension/CLI 贯穿 daemon、VFS 和 provider；日志过滤秘密和签名 URL。
- 未知 size 的导出对象不能作为普通文件暴露；先通过显式 Kind/导出策略处理。

## 缓存细节裁决

- cache block 默认不做每块 `fsync` 和目录 `fsync`。临时文件完整关闭后原子 rename；崩溃留下的截断文件由 header/payload 校验删除。仍提供保守的 fsync 模式用于测试或特殊部署。
- 每个 payload 在进程内第一次命中时完整校验一次，索引记 `verified`；不是每次小读都重新哈希。
- 启动扫描可以先验证 header 和大小，payload 延迟到首次命中；扫完前按预算已满处理，避免启动时超额写入。
- 不使用 bbolt 维护基本块索引；文件名就是 key，目录自身充当索引。后续只有在明确规模瓶颈时才增加数据库。
- `entries/<namespaceTag>/<keyHash前两位>/<keyHash>.blk` 做 fan-out。
- LRU 可从 block 文件 mtime 恢复，命中时批量 touch，避免每次小读写 metadata。
- 打开 block 必须通过受控 root、no-follow 和打开后 fstat 验证，不能先 lstat 再按路径打开。
- 明文 owner-only cache 作为默认；逐账户可关闭持久缓存。当前不增加 per-cache encryption，前提是威胁模型明确排除恶意同用户进程。

## 平台标识裁决

- FSKit 在 daemon 持久映射完成前立即把 `supportsPersistentObjectIDs` 设为 `false`。
- FUSE 使用 NodeKey 到平台节点的显式映射，不复用 bare ObjectID 的 hash。
- 是否令 `st_ino == nodeid` 先通过 go-fuse 行为测试确认；协议 nodeid 和用户可见 `st_ino` 分开建模，不把未验证假设写进契约。
- FSKit volume UUID 使用 `UUIDv5(namespace version, AccountID + normalized scope)` 确定性派生。
- 同一 AccountID + scope 重复挂载默认拒绝；不同 scope 可以有不同 volume。

## Provider 接口裁决

不再设计一套平行、需要长期维护的 v2 interface。采用可选能力接口：

```go
type RevisionReader interface {
    OpenReadRevision(context.Context, ObjectRevision) (Reader, error)
}

type ReadCapabilities interface {
    ReadCapabilities(context.Context) (RevisionCapabilities, error)
}
```

- 旧 `Session` 继续可用于当前版本读取，但缺失 revision 接口时持久缓存默认关闭。
- factory 声明能力上限，session 探测后只能收窄。
- 单个对象通过 Kind、空 ContentVersion 或明确的 no-cache 标志退出持久化。
- broker wire protocol 只传 NodeKey/ID 和期望 revision；daemon 根据可信 session 状态解析 Object，不接受 extension 提供的完整远端 Object 元数据作为权威。
- 凭据库 item key 迁移到 AccountID，账户改名不再需要迁移凭据项。
- AccountID 旁边保存带版本的、不可逆 binding fingerprint；fingerprint 字段集由 provider 显式枚举，加载时校验，不参与随机 ID 派生。

## 对十个待定项的正式决定

| # | 决定 |
| --- | --- |
| 1 | 拆分 ContentVersion 和可选 MetadataVersion；MetadataVersion 缺失时，VFS 只用于弱 metadata 缓存。 |
| 2 | 每个远端请求绑定版本；revision read、经过实测的 strong If-Match GET，或不可变对象存储的等价证明可以启用缓存。前后 Stat 不够。 |
| 3 | factory 声明上限，session 探测收窄，对象级可退出。 |
| 4 | fingerprint 存在 AccountID 旁，不参与派生；字段集版本化并显式列举。 |
| 5 | FUSE 用显式 node map；FSKit 先关闭 persistent IDs，之后由 daemon 持久映射或保持关闭。 |
| 6 | 单 daemon 单 cache owner；不同 cache root 才能并行。 |
| 7 | 文件系统作为初版索引；后台启动扫描，扫描期间保守限制写入。 |
| 8 | owner-only 明文缓存可接受，逐账户可关闭；加密延后。 |
| 9 | macOS 默认按账户挂载 volume；aggregate 先用于 CLI/WebDAV。名称冲突拒绝，不做隐式转义。 |
| 10 | 用显式 Kind/能力表达 Document、Shortcut、Export；不能伪装成未知大小的普通文件。 |

## 非逐字采纳的部分

- FUSE `st_ino = nodeid` 暂不作为契约。先把协议 nodeid、目录节点和 `st_ino` 的行为用 go-fuse 测试固定下来。
- Baidu 小文件摘要缓存必须以其 XPAN MD5 的实测可靠性为前提；在此之前仍默认关闭。
- 启动全量计算 payload hash 不作为默认方案；它会放大重启 I/O。header/大小启动检查加首次命中的 payload 校验更合适。
- 持久缓存仍必须严格验证精确 revision；摘要只补强“provider 的版本令牌没有如实变化”这一故障，不改变读取绑定门槛。

## 新的验收顺序

1. FSKit 不再虚假声明持久对象 ID；本机挂载不因撤回声明而退化。
2. 账户迁移后 AccountID、凭据项和 binding fingerprint 重启稳定。
3. extension 重启后无需 CLI 在场即可重新连接 daemon；读取响应全程为二进制。
4. provider conformance 证明每次远端请求都绑定同一 ContentVersion；冲突不会混入任何字节。
5. 元数据和内存基线满足 Finder 场景，块大小、预读和 TTL 有测量依据。
6. 重启后的缓存命中、损坏恢复、LRU、并发填充和账户隔离全部通过。
7. 只有以上门槛完成后才默认启用磁盘缓存。

详细工程任务见 [TODO.md](../TODO.md)。
