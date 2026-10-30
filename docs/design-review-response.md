# FuseDock 设计审阅响应

更新时间：2026-10-28。工程任务与验收以 [TODO.md](../TODO.md) 为准；独立审阅原稿保留在 [FuseDock 设计审阅意见.md](../FuseDock%20设计审阅意见.md)。本文记录本轮讨论后的正式决定。

## 文件系统抽象与读取快照

FuseDock 的目标是提供文件系统抽象。不可变读取快照是合法的一致性模型，保留在设计中。普通 `open()` 本身没有快照保证这一事实，不构成弱化 FuseDock 快照契约的理由。

打开时选定的内容版本和大小贯穿后续读取、EOF、句柄属性、重连和关闭。目录改名保持对象身份；同路径对象替换影响新的路径解析。关闭释放 reader，再次打开按刷新策略选择版本。平台原生缓存、同对象不同版本的并发打开、FSKit item 生命周期都必须满足同一契约。

使用正常平台缓存并实现失效与刷新。删除以 direct I/O 建立基线的实施要求。无版本绑定能力的 provider 明确提供普通当前版本读取，不能由此推导强版本缓存资格。

## 完整 daemon 与平台解耦

完整交付 daemon 的会话、控制 API、二进制数据面、peer 认证、启动恢复、挂载生命周期、重新认证、缓存管理、诊断和关闭。按职责组织任务，取消缩减功能后逐阶段补齐的交付方式。

provider、namespace、VFS、cache 和服务契约与平台代码隔离。公共平台接口通过 Go 条件编译路由到 Linux FUSE、macOS FSKit 和 Windows WinFsp；Swift 和平台 SDK 类型留在 adapter 内。

测试分为通用契约、go-fuse 库行为、Linux 原生挂载、macOS FSKit、Windows WinFsp。它们独立运行并报告结果，产品范围保持完整。daemon、extension、volume 和已打开读取的恢复是不同验收项。

go-fuse 的同 StableAttr 去重行为已通过 macOS 临时内存接口测试复现：两个不同对象返回相同 node ID 2、inode 424242。该结果无需 `/dev/fuse`，与 Linux 实机挂载验收分开。

## 内容版本与两种缓存证明

对同一个 NodeKey，相同非空 ContentVersion 对应相同大小和字节。不同对象可以共享 ContentVersion，缓存键仍包含完整 NodeKey。

| 路径 | 证明要求 |
| --- | --- |
| 精确 range 填充 | 每个远端请求绑定打开快照；条件 GET 使用 strong If-Match，并验证 206、Content-Range、总大小和响应版本 |
| 整文件声明摘要 | 元数据声明的摘要嵌入 ContentVersion；完整下载后比对原始打开快照；验证成功再交付字节和发布缓存 |

整文件路径保留五条规则：provider 声明、原始打开快照、完整对象覆盖、不匹配返回 ErrConflict 且不发布、修改/重复内容/并发测试通过。它无需远端 range 能力，也不授予未经验证的 range 缓存资格。

整文件校验是可选路径，不能成为所有文件打开的必经成本。它带来的首字节等待和下载放大需要单独计量；已验证的完整本地条目可以服务任意切片。

## 资源与缓存算法

全局预算覆盖保留块、填充缓冲、IPC 缓冲、metadata、节点/reader 表及临时磁盘数据。通过预算预留与并发控制管理分配，关闭和取消释放资源。

缓存采用按字节计费的 2Q：首次访问 FIFO、重复访问保护队列和有界 ghost 历史。合并同一顺序访问中的相关小读，避免扫描被误判成长期热点。provider 获取粒度与持久块大小分开；连续读取扩大有界预取，seek 后缩小。

计数器位于公共 provider/VFS 边界。用 Finder、QuickLook、媒体 seek、并发读取和顺序浏览测量首字节延迟、网络放大、命中和内存。TTL、块大小、预取及整文件阈值由这些测量确定。

文件索引保留目录 fan-out、后台 stat 扫描、首次命中的 header/payload 校验、原子发布和批量 mtime 更新。缓存写入失败不改变已验证读取的结果。

## 身份与路径

新账户使用随机 AccountID 和独立的 CredentialRef；账户别名变化不移动凭据。binding 字段明确列举并带版本。

配置的 scope 路径在每次挂载时解析，当前挂载持有解析出的根 NodeKey。重新挂载重新解析配置路径。volume 身份由账户和配置 scope 确定，数据缓存使用实际对象和内容版本。跨重启永久追随旧目录对象不作为默认行为。

## WebDAV 与账户兼容的最终优先级

现有 WebDAV 实现、测试、CLI 参数、broker 路由和 WebDAV 包引用已硬删除。当前工作不保留兼容桩，不构建替代实现，也不以维持编译作为删减范围的约束。

WebDAV 重建、WebDAV client provider 和旧网盘账户迁移置于计划末尾，优先级最低。完整文件系统实现不依赖这些功能。

旧账户兼容的实际目标是保留现有登录。将来实现时，在原子更新的配置中添加 AccountID，原凭据定位值保留为固定 CredentialRef；新账户使用 AccountID 型引用。无需复制或重命名 Keychain 项，也无需跨存储迁移日志。

## 参考依据

- [Linux FUSE I/O](https://docs.kernel.org/filesystems/fuse/fuse-io.html)：原生缓存与 direct I/O 的行为差异。
- [rclone VFS cache](https://rclone.org/commands/rclone_mount/#vfs-cache-mode-full)：按需缓存和 metadata fingerprint；整文件下载不是普通挂载读取的统一要求。
- [2Q 原论文](https://www.vldb.org/conf/1994/P439.PDF)：缓存准入、扫描污染与有界历史。
- [open(2)](https://man7.org/linux/man-pages/man2/open.2.html)：路径解析和打开对象引用的区别。
