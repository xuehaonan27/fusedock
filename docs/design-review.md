# FuseDock 设计审阅总稿

| 文档日期 | 2026-10-24 |
| --- | --- |
| 对应代码快照 | `620a358cb4ae7116fac966962e8679f446403075` |
| 审阅目的 | 让没有项目上下文的独立审阅者评估 FuseDock 的整体设计，重点判断 namespace、版本语义、持久缓存和跨平台挂载边界是否足以支撑后续实现。 |

2026-10-26 的独立审阅已审阅本文快照 `620a358`。本文已根据审阅意见修正 ContentVersion 作用域、逐请求读取绑定、FSKit 生命周期、缓存写盘策略和平台标识方案；接受与未逐字采纳的裁决见 [design-review-response.md](design-review-response.md)。项目执行顺序以 [TODO.md](../TODO.md) 为准。

## 0. 阅读说明

这份文档是一份自包含的设计快照，不要求读者先阅读仓库或其他文档。文中使用四种状态：

| 标记 | 含义 |
| --- | --- |
| 已实现 | 当前代码和测试中已有对应行为 |
| 已决定 | 已作为当前架构方向采用，但可能尚未全部落地 |
| 建议 | 尚未实现，本文建议采用，仍在独立审阅中 |
| 待定 | 存在实质取舍，需要审阅者重点判断 |

文中尤其需要区分两件事：

1. 当前 provider 只有一个 `Object.Version` 字段，持久缓存尚未实现。
2. “拆分内容版本与元数据版本”是本轮提出的重要改进，不属于已实现代码。

### 0.1 审阅者先行结论

建议先按以下五条阅读：

1. FuseDock 的核心分层——provider、namespace/VFS、platform surface——方向合理，已有真实 PKU/Baidu、FSKit 和 WebDAV 链路支撑。
2. 当前是只读架构基线，不是生产级完整网盘客户端；写入、持久缓存、多账户、Windows 和正式发行都未完成。
3. 在持久缓存之前必须先解决四件事：不可变 AccountID、跨重启稳定 ObjectID、强 ContentVersion、读取与版本的实际绑定。
4. 最大的开放设计题是版本模型，其次是 FSKit/FUSE 的平台标识映射和单一 daemon/cache owner 的生命周期。
5. 最严格的正确性验收问题是：给定 `AccountID + ObjectID + ContentVersion + range`，能否证明返回的是同一个对象版本的连续字节。

## 1. 项目摘要

FuseDock 是一个跨平台、provider-neutral 的云存储本地文件系统项目。目标是把不同网盘、对象存储和标准协议统一成一个小型只读文件契约，再通过 macOS FSKit、Linux FUSE、Windows WinFsp、WebDAV、CLI 和未来的 GUI/移动端提供访问。

项目的起点受到 [`pkudisk-fuse`](https://github.com/thezzisu/pkudisk-fuse) 启发，但目标不是复刻单一高校网盘挂载器。FuseDock 希望成为通用基础设施：

- 同一个 provider 同时服务 CLI、原生文件系统、WebDAV 和应用内浏览。
- 每个 provider 自己处理认证、分页、对象 ID、版本和字节范围差异。
- 上层不解析供应商路径，不把显示名称当作持久身份。
- 先建立只读正确性，再增加多账户、持久缓存、写入和发行体系。
- macOS 使用纯用户态 FSKit，不依赖 macFUSE 或内核扩展。

当前仓库是一个可运行但尚不完整的只读预览。PKU 网盘和百度网盘已完成真实账号的读取链路；macOS FSKit、Linux FUSE 和 WebDAV 都存在代码路径；持久缓存、多账户、写入、Windows 挂载、签名发行和生产级守护进程仍未完成。

### 1.1 当前最有价值的成果

- `pkg/provider` 提供统一的认证后会话语义，Linux、CLI、WebDAV 和 macOS 共用 provider/VFS 核心。
- PKU 已经完成 IAAA 登录、可恢复会话、目录遍历和 revision-bound range read。
- 百度已经完成 OAuth device flow、XPAN 分页、`fs_id` 对象身份、range read 和 refresh-token 轮换。
- 密码与令牌已存入系统凭据库；账户元数据不保存秘密。
- 沙箱中的 FSKit 扩展通过一次性 broker capability 访问只读数据，不直接读取 Keychain。
- WebDAV 已通过 Finder 实测，支持 Basic/Bearer 认证、DAV class 1、PROPFIND、GET、HEAD、Range 和 ETag，写操作明确拒绝。

### 1.2 仍需解决的核心问题

- 账户仍以可变的本地名称作为唯一标识，无法安全地跨重启共享缓存。
- provider 对象 ID 目前只承诺在 session 生命周期内稳定，尚不足以支撑持久 inode。
- 单一 `Version` 字段同时承担内容校验、元数据失效、写冲突和 WebDAV ETag，语义不够强。
- broker 数据面直接调用 provider，WebDAV 又创建了一层 metadata VFS；未来需要唯一共享 VFS。
- FSKit 当前声明 `supportsPersistentObjectIDs = true`，但底层契约还不满足这一承诺。
- 多账户会暴露 bare object ID 生成 inode 的碰撞问题。
- 还没有 daemon、认证控制面、持久块缓存、崩溃恢复或写入语义。

## 2. 目标、范围与非目标

### 2.1 产品目标

1. 在 macOS、Linux、Windows 和协议层面提供一致的文件浏览与读取体验。
2. 让新 provider 只需实现小契约，而不必重复实现每种文件系统前端。
3. 支持北大网盘、百度网盘、Google Drive、OneDrive、S3 兼容存储、WebDAV 和其他可扩展后端。
4. 允许多个账户同时存在于一个逻辑 namespace 中，同时保留单账户挂载。
5. 为浏览器、桌面 GUI 和移动端提供与挂载相同的 provider/VFS 能力，而不要求它们实现内核文件系统。
6. 在数据正确性、安全边界和可诊断性上达到长期基础设施项目的要求。

### 2.2 当前平台范围

| 平台/入口 | 状态 | 说明 |
| --- | --- | --- |
| macOS FSKit | 已实现，已做过真实挂载验收 | 纯用户态扩展，不使用 macFUSE |
| Linux FUSE | 已实现，待原生 FUSE 主机完整验收 | 使用 go-fuse，需要 FUSE 3 和 `/dev/fuse` |
| WebDAV | 已实现，Finder 实测成功 | 只读、loopback、Basic/Bearer、Range |
| CLI | 已实现 | provider 发现、账户管理、ls、mount、broker |
| Windows WinFsp | 未实现 | Go 核心和 CLI 已可交叉编译 |
| 浏览器应用 | 通过 WebDAV 初步可用 | 只允许明确的 origin |
| 移动端 | 未实现 | 计划复用 provider/VFS，不做内核挂载 |

### 2.3 明确非目标

- 不做离线双向同步或透明冲突合并。
- 不把本地文件系统变成远端规范的唯一副本。
- 不承诺 POSIX ACL、xattr、硬链接、远程锁或协作编辑。
- 不把 Google Docs、百度网盘特殊对象和普通二进制文件伪装成语义完全相同的文件。
- 不在第一阶段做动态下载和安装 provider 插件。
- 不把 OpenList 等 AGPL 项目的内部驱动直接嵌入项目；第三方代码必须满足许可证和可维护性要求。

## 3. 当前实现状态

| 领域 | 状态 | 证据/边界 |
| --- | --- | --- |
| Go 版本 | Go 1.24+ | `go.mod` |
| Provider 契约 | 已实现，ContractVersion 1 | `pkg/provider` |
| Local provider | 已实现 | `os.Root` 约束，适合做 fixture |
| PKU provider | 已实现只读预览 | 真实账号验证；IAAA、AnyShare、range |
| Baidu provider | 已实现只读预览 | 真实账号验证；OAuth device flow、XPAN |
| 账户存储 | 已实现 v1 | 元数据 JSON + 系统凭据库 |
| 账户 broker | 已实现只读协议 | 一次性 loopback capability |
| Linux FUSE | 已实现 | 原生挂载验收仍列为 TODO |
| macOS FSKit | 已实现 | Swift extension + Go c-archive |
| WebDAV | 已实现 | 只读 DAV class 1，Finder 实测 |
| 持久块缓存 | 建议/设计 | 当前只有 per-handle 内存块 |
| 多账户 namespace | 建议/设计 | 当前一次 session 一个账户 |
| 写入 | 未实现 | 只有未来设计 |
| Windows 挂载 | 未实现 | 计划 WinFsp/cgofuse |
| Google Drive | 未实现 | 计划 PKCE + stable IDs |
| OneDrive | 未实现 | 计划 Microsoft Graph |
| iCloud Drive | 未设计 | 需要单独的可行性研究 |

## 4. 术语与全局不变量

### 4.1 术语

| 名称 | 含义 |
| --- | --- |
| Account | 一个保存的 provider 认证绑定 |
| AccountID | 账户的不可变本地身份，不能由别名派生 |
| Provider Session | 一个已认证的远端会话，对应一个账户 namespace |
| ObjectID | provider 给出的对象身份，在账户内唯一 |
| ContentVersion | 建议中的强内容验证器；相同值必须对应逐字节相同内容 |
| MetadataVersion | 建议中的元数据状态；可变但不影响内容块复用 |
| NodeKey | AccountID + ProviderID + ObjectID 的本地节点身份 |
| Surface Path | 用户看到的挂载或 WebDAV 路径，只用于呈现和查找 |
| VFS | 元数据合并、读取规划、缓存和未来的写入恢复层 |

### 4.2 必须保持的不变量

1. 显示名称和路径不能成为持久对象身份。
2. 两个账户即使返回相同 ObjectID，也必须是两个不同对象。
3. 同一个对象改内容时，身份可以不变，但内容版本必须变化。
4. 缓存命中不能把旧版本内容当作新版本返回。
5. provider 返回的文件名必须经过单路径组件验证；分页必须在 adapter 内闭合。
6. `ReadAt` 必须接受每次调用自己的 context，不能复用 `OpenRead` 已返回的 context。
7. 缓存是优化；缓存损坏或磁盘写失败不能使一次本来有效的远端读取失败。
8. 秘密只进入 CLI/broker/daemon 的受信任进程，不进入 FSKit sandbox、URL、日志或缓存文件。

## 5. 分层架构

### 5.1 总体形状

```text
CLI / FSKit / Linux FUSE / WinFsp / WebDAV / GUI / Mobile
                         |
                  surface adapter
                         |
                 namespace + VFS
          metadata cache + read planning
             optional persistent cache
                         |
                  provider.Session
                         |
      local / PKU / Baidu / Drive / OneDrive / S3 / WebDAV
```

各层职责：

| 层 | 职责 |
| --- | --- |
| Provider | 认证、命名空间遍历、对象身份、版本、分页、range read |
| Namespace | 将账户绑定和远端对象组合成稳定 NodeKey，解析表面路径 |
| VFS | 元数据 TTL、并发合并、读取规划、缓存、未来的写入恢复 |
| Surface | 翻译成本地文件系统 ABI、WebDAV 或应用 API |
| Host | FSKit extension、FUSE 进程、WinFsp 服务、WebDAV 服务、GUI |

### 5.2 关键设计选择

- 先做只读契约，避免把尚未解决的远端写入冲突带入第一版。
- provider 不依赖 FUSE、FSKit 或 HTTP 类型，保证同一后端可复用。
- FSKit 通过受限 broker 获取数据，凭据留在非沙箱进程。
- WebDAV 不是 provider 的替代品；它是与原生挂载并列的 surface。
- 持久缓存不直接插在 provider 内，而是共享 VFS 的一层，避免每个 provider 重写缓存协议。
- 未来由单一 daemon 拥有 provider sessions、缓存和挂载生命周期，防止多个进程争用同一缓存根。

## 6. Provider 契约

### 6.1 当前接口

```go
type Session interface {
    Root(context.Context) (Object, error)
    Stat(context.Context, Object) (Object, error)
    List(context.Context, Object) ([]Object, error)
    OpenRead(context.Context, Object) (Reader, error)
    Close() error
}

type Reader interface {
    ReadAt(context.Context, []byte, int64) (int, error)
    io.Closer
}
```

Factory 通过 descriptor 声明 provider ID、名称、选项和凭据字段。Registry 在进程内注册 factory；非 Go sidecar 仍是未来设计。

### 6.2 当前对象模型

```go
type Object struct {
    ID       string
    ParentID string
    Name     string
    Kind     Kind
    Size     int64
    Version  string
    Modified time.Time
    Created  time.Time
}
```

当前注释要求 `ID` 在 session 内稳定且唯一，`Version` 在可读内容变化时改变。这个保证对单次挂载够用，但不足以支撑跨重启持久缓存。

### 6.3 当前 provider 不变量

- `Root` 返回目录，不执行 vendor-specific 路径查找。
- `List` 返回一个目录的直接子项完整快照，分页在 adapter 内完成。
- 每个 `Name` 是合法单组件，不允许 `/`、`\`、NUL、`.` 或 `..`，同目录不得重名。
- `ReadAt` 不静默返回零字节；短读、越界和 content-range 不匹配必须报错。
- session 对并发 metadata 和 open calls 安全。
- 常见错误统一映射为 `ErrNotFound`、`ErrPermission`、`ErrConflict`、`ErrUnsupported` 等。

### 6.4 建议中的能力声明

持久 inode 和数据缓存需要 provider 明确保证以下能力，而不是将所有 provider 一视同仁：

| 能力 | 含义 |
| --- | --- |
| StableObjectID | 同一账户绑定下，ID 跨 session 重启和改名保持稳定 |
| StrongContentVersion | 相同版本令牌对应逐字节相同内容 |
| ExactRevisionRead | `OpenRead` 能读取请求版本，或返回 `ErrConflict` |
| ReliableRangeRead | 返回严格符合请求区间和总大小的字节 |

前三项是持久数据缓存的门槛，`ReliableRangeRead` 是任何 range 缓存的门槛。WebDAV 等协议的能力可能随服务器变化，因此建议在 session 层探测，而不仅是在 factory 上静态声明。

### 6.5 当前 provider 对比

| Provider | 对象身份 | 内容版本 | 精确读取 | 持久缓存建议 |
| --- | --- | --- | --- | --- |
| Local | 当前为相对路径，改名会变 | mtime+size | 普通文件句柄，不稳定快照 | 默认绕过 |
| PKU | `gns://` document ID | AnyShare `rev` | `osdownload` 绑定 `rev` | 满足一致性测试后启用 |
| Baidu | `fs_id` | mtime+size+MD5 拼成的字符串 | dlink 未证明绑定元数据 revision | 暂不启用 |
| WebDAV client | 待实现 | 取决于服务端 ETag 强度 | 可能只能 If-Match/前后 Stat | 逐服务器探测 |
| S3 | bucket+key / versionId | ETag/versionId | versionId 强绑定 | 最合适的目标之一 |
| Google Drive | file ID | md5/version 字段，需验证 | 需要确认 revision 读取能力 | 待 provider 验证 |
| OneDrive | driveItem ID | cTag/eTag，需验证 | 需要确认条件读取能力 | 待 provider 验证 |

## 7. 账户、凭据与 broker

### 7.1 当前账户存储（已实现 v1）

账户元数据包含本地名称、provider ID、username、options 和创建时间。元数据保存为 owner-only JSON；密码、OAuth 凭据和 provider 状态保存在系统凭据库：

| 平台 | 凭据后端 |
| --- | --- |
| macOS | Login Keychain |
| Linux | Secret Service |
| Windows | Credential Manager |

凭据库 service 为 `dev.fusedock.accounts`，当前以本地账户名称作为 item key。provider 状态是不透明 JSON，保存前会绑定 provider 与 username，降低误写其他账户状态的风险。

当前这一模型存在一个必须解决的迁移问题：账户名可变，但凭据项以账户名为 key。目标迁移把凭据项改到 AccountID key，并幂等地迁移旧 item；这样账户改名不再移动秘密。

### 7.2 建议中的 AccountID

每个账户增加不可变的 `acct_` + 128-bit 随机 ID。ID 不由名称、provider、username、endpoint 或密码哈希派生。账户绑定至少包括：

```text
provider ID + normalized endpoint + remote principal + identity-affecting options
```

- 修改显示别名、默认账户、密码或 token，不改变 AccountID。
- 修改 provider、endpoint、username、远端主体或身份相关 options，必须轮换 AccountID。
- 删除后同名重建必须获得新 ID。
- 无法获取稳定远端主体时，可以退回 username，但要明确降低身份保证。
- OAuth provider 更适合把 issuer + openid/subject 作为远端主体。
- AccountID 只是本地 namespace 标识，不是认证能力；知道它不能访问任何账户。
- CLI 的 JSON 输出和诊断应暴露 AccountID，自动化需要跨改名保持稳定时应使用它。

provider 显式声明并版本化这组 binding fields。具体 fingerprint 存在 AccountID 旁边，用于加载时防御性校验，不参与随机 ID 派生，也不保存密码或可逆秘密。fingerprint 不匹配时 fail closed，要求重新认证。

### 7.3 FSKit account broker（已实现）

线程和信任边界：

```text
system credential store
        | 仅 CLI/broker 进程读取
    broker process
        | 127.0.0.1 + 一次性 capability
  FSKit extension
        |
 kernel / Finder
```

控制项：

- 只绑定 literal loopback，不监听局域网地址。
- bootstrap capability 写入 `0600` 文件，成功 claim 一次后换成只存在内存中的 256-bit session token。
- broker 只有 root/list/lookup/stat/read/close，没有账户管理或写路由。
- 所有 broker API 使用 POST JSON，拒绝 Origin/Referer、query string 和非预期 Host。
- 请求体、header 和单次读取都有上限。
- provider 错误固定映射，不回传远端错误文本、签名 URL 或令牌。
- WebDAV 使用独立 token，不能复用 FSKit 的 bootstrap capability。

当前残余风险：同一 OS 用户的其他进程仍可能在扩展 claim 之前读取私有 capability 文件。它能获得该账户的只读访问，但不能获得密码、token、写权限或其他账户。最终方案计划改成 Unix domain socket/XPC，并加入 peer code-signing、launchd 生命周期和更严格的本地控制面。

### 7.4 WebDAV 认证

- 服务只监听 loopback。
- 使用独立 Basic 密码或 Bearer token；auth 文件 owner-only。
- CORS 只接受明确 origin，不开通配符。
- 写方法、LOCK/UNLOCK 明确返回拒绝。
- Finder 的“不允许任何额外用户登录”曾排查为 NetAuthAgent 的 `ECONNREFUSED`，通常是 broker 已停止或 Finder 收藏了旧的随机端口。固定 loopback 端口可以缓解地址漂移。

## 8. Namespace 契约

### 8.1 三个 namespace 必须分开

| Namespace | 回答的问题 | 稳定标识 |
| --- | --- | --- |
| Account | 这是哪个保存的认证绑定？ | AccountID |
| Remote object | 这是哪个 provider 对象？ | AccountID + ProviderID + ObjectID |
| Surface path | 用户从哪里看到它？ | 无，路径只是呈现 |

### 8.2 NodeKey

建议类型：

```go
type NamespaceKey struct {
    AccountID  AccountID
    ProviderID ProviderID
}

type NodeKey struct {
    Kind       NodeKind
    Namespace  NamespaceKey
    ObjectID   string
}
```

NodeKey 的编码包含账户、provider、对象 ID 和节点类型。版本不进入 NodeKey，因为内容更新不应改变对象身份。

### 8.3 单账户与聚合视图

- 单账户视图把所选 provider root 或 scope 映射到挂载 `/`。
- 聚合视图在 `/` 下用账户别名显示各账户，例如 `/pku`、`/baidu`、`/onedrive`。
- provider ID 不是默认顶层目录；同 provider 的两个账户也必须独立。
- 两种视图中的同一个远端对象必须解析到同一个 NodeKey，只是 surface path 不同。
- 账户别名只用于 presentation，重命名不改变对象身份。

### 8.4 路径与名称

- 路径解析一律通过 `List` 逐级匹配名称，不直接把路径拼进供应商 API。
- 账户别名建议大小写不敏感唯一，并执行 Windows 保留名、结尾点/空格、控制字符检查。
- `.fusedock` 保留给虚拟 namespace。
- 如果两个远端名称在目标文件系统的大小写/Unicode 规范化规则下冲突，不应静默合并；需要明确错误或未来的转义层。
- rename 路径只改变显示位置，不改变 NodeKey 和数据库缓存键。

### 8.5 inode / FSItem identity

平台原生标识必须从完整 NodeKey 派生，而不是只哈希 `Object.ID`。

当前状态：

- Linux FUSE 当前只用 `Object.ID` 生成 inode。
- FSKit 当前用 FNV-1a 只哈希 `object.id`，且声明了 `supportsPersistentObjectIDs = true`。
- 这只在单账户、单 session 假设下勉强成立，多账户时同 provider 对象 ID 会碰撞；跨重启 provider 也不保证 ID 稳定。

审阅后方案：

1. FUSE 维护自己的 NodeKey→node map，节点 ID 在一个挂载内稳定；协议 nodeid 和用户看到的 `st_ino` 分开验证。
2. 不把 hash 本身当作持久查找结构；映射表检测碰撞，不能把两个节点当作同一个。
3. 在 StableObjectID 和持久找回能力完成前，立刻把 FSKit 的 `supportsPersistentObjectIDs` 设为 `false`。
4. 恢复真实持久 ID 时，由 daemon 保存 NodeKey→平台 ID 的映射，并能从收到的 ID 反查对象。
5. FSKit volume UUID 从版本化 `AccountID + normalized scope` 确定性派生，同一目标重复挂载拒绝。

### 8.6 迁移与轮换

账户配置需要从当前 v1 原子迁移：

1. 获取账户仓排他锁。
2. 严格读取旧配置，不生成临时 ID。
3. 校验新别名规则和大小写冲突。
4. 每个账户生成随机 ID、计算显式 binding fingerprint，并检查碰撞。
5. 幂等地把凭据库 item 从 alias key 迁移到 AccountID key；旧 item 在新配置提交前保留。
6. 写临时文件、`fsync`、原子 rename、同步目录。
7. 只有新配置和凭据查找持久化成功后才释放锁；失败必须保持旧配置可读。

旧的 cache、inode 和 capability 不能跨绑定变更继续使用。清理可以 best effort，但不能再访问。

### 8.7 进程与 capability 边界

- 每个账户拥有独立的 provider session、refresh lock、metadata cache、node table 和 cache namespace；不能把多个账户的 bare ObjectID 放进同一个无前缀 map。
- broker data capability 必须绑定一个 MountTarget。服务器自行附加或校验账户 namespace，客户端不能通过修改请求里的 ObjectID 跨账户读取。
- FSKit 和 WebDAV 暴露相同目标时，可以共享同一个 provider/VFS session，不能各自创建竞争缓存。
- 删除或重绑账户后，旧 capability 和 open handle 不能继续落到同名新账户。
- 当前 broker protocol 已绑定进程内单一 session；多账户需要协议版本升级，而不是把完整 Object 当作可信客户端输入。

## 9. 版本语义

### 9.1 当前问题

当前单一 `Object.Version` 同时承担多种职责：

- 判断本地文件内容是否需要重新读取；
- 生成 WebDAV ETag；
- 组织持久缓存键；
- 未来作为写冲突判断的 expected version；
- 在目录/属性变化时表示 metadata 状态。

这些职责的要求并不相同。尤其是持久缓存需要很强的内容承诺，而 mtime、size、弱 ETag 或“供应商叫 version 的字段”通常只能提供较弱的新鲜度提示。

### 9.2 建议的核心不变量

把内容版本定义为不透明的强验证器：

```text
对同一个 AccountID + ProviderID + ObjectID：
(ContentVersion 非空且相同)
=> Size 相同且对应字节内容完全相同

同一对象的可读字节发生变化
=> ContentVersion 必须变化
```

不需要规定版本递增、可比较或可解析。不同 ContentVersion 可以对应相同内容，最坏结果只是缓存未命中。反过来，两个不同对象也可以合法地共享同一个 ContentVersion，尤其是内容摘要型 token；缓存身份仍由完整 NodeKey 隔离。

### 9.3 建议的对象模型

```go
type Object struct {
    ID              string
    ParentID        string
    Name            string
    Kind            Kind
    Size            int64
    ContentVersion  string // 强内容验证器；为空表示不能持久缓存
    MetadataVersion string // 名称、时间、权限等元数据状态
    Modified        time.Time
    Created         time.Time
}
```

| 字段 | 回答 | 变化影响 |
| --- | --- | --- |
| ID | 是哪个对象 | 对象身份 |
| ContentVersion | 是哪一版内容 | 数据缓存、读取一致性 |
| MetadataVersion | 元数据状态是什么 | 属性/目录缓存，不影响已缓存内容块 |
| Size | 内容长度 | range 校验和缓存键 |

目录不需要 ContentVersion；目录 metadata 只需要 MetadataVersion。Google Docs 导出、云端特殊对象等如果无法保证强内容版本，可以留空并跳过持久数据缓存。

### 9.4 读取绑定

“有版本号”和“能读取指定版本”必须分开。建议在 session 层明确模式：

| 模式 | 保证 | 持久磁盘缓存 |
| --- | --- | --- |
| Revision-bound read | 直接请求指定 revision，或返回 conflict | 可以 |
| Conditional GET | 服务端对每个请求用 If-Match 和 strong ETag 验证版本，并返回可校验的 206 | 通过实测后可 |
| Response version check | 只对不可变对象存储使用，响应版本必须等于请求版本 | 有条件允许 |
| Whole-object declared-digest check | 完整下载后，与**打开快照时** provider 声明并嵌入 ContentVersion 的摘要一致 | 仅限整文件路径 |
| Stat before/after read | 只能缩小竞态窗口，无法证明全程未变 | 不建议 |
| Current revision only | 始终读最新内容 | 禁止 |

`OpenRead(snapshot)` 应该接收 `ObjectID + ContentVersion + Size` 的版本快照。它要么返回该版本，要么返回 `ErrConflict`；不能在内部悄悄重新 Stat 并切换到新版本。若上层希望读新版本，只能创建新的 snapshot 后重试。

绑定粒度是**每个远端请求**，而不是一次 `OpenRead`。一个 Reader 的每次 `ReadAt` 可能发出多个请求、转发多个重定向或续签多个签名 URL；这些请求全部必须携带并验证同一 ContentVersion。PKU 每次 `osdownload` 和对象 GET 都绑定同一 `rev`，rev 不存在就返回冲突。

条件请求使用 `If-Match`，不使用 `If-Range` 作为版本门槛。`If-Range` 不匹配时服务端可以忽略 Range 并返回 200 全文。每个响应必须验证状态码为 206、`Content-Range` 的起点/终点/总大小正确、响应 ETag 等于请求版本；忽略条件、返回 200 或任何不一致都应转换为 `ErrConflict`，对已经返回的字节不能静默切换 revision。

整对象下载后与声明摘要校验，可以证明“下载到的字节等于 provider 在快照版本下声明的内容”。这不需要知道服务端实际返回了哪个 revision；只读缓存只要求相同 `(ObjectID, ContentVersion)` 对应相同字节。该路径必须同时满足：

1. 摘要来自 provider 元数据并嵌入 ContentVersion；本地计算的摘要不能单独构成版本。
2. 比较对象是打开快照时保存的声明摘要，不是下载后重新 Stat 得到的摘要。
3. 只对完整对象下载生效；range 读取不算，继续要求 If-Match 或 revision-bound read。
4. 摘要不一致返回 `ErrConflict`，绝不能写入缓存。
5. provider 的摘要可靠性必须先通过修改、重复内容和并发场景测试。

这只适用于整文件路径，不能推广成 range 缓存的证明，也不能替代未来写入所需的 expected-version。

### 9.5 方案比较

| 方案 | 优点 | 缺点 | 结论 |
| --- | --- | --- | --- |
| 保持单一 Version，只加强注释 | SDK 改动最小 | 无法表达强弱保证，弱 provider 容易被误用；metadata 和 content 变化混在一起 | 仅适合过渡 |
| 单一 Version，但强制所有 provider 都提供强语义 | 模型最简单 | mtime/size 型 provider 无法合规；metadata 变化会误伤内容缓存 | 可作为 provider v2 的兼容层 |
| 拆分 ContentVersion/MetadataVersion，并新增读取绑定能力 | 语义清晰，可按 provider/对象降级；适合写入冲突 | 需要 provider contract v2、协议升级和 conformance | 推荐 |
| 用本地计算摘要直接充当版本 | 内容相等判断直接，可辅助完整性 | 多数服务不提供摘要；自己算需先下载；MD5 有碰撞问题；仍不能读旧版本 | 只能作完整性辅助 |
| 整对象下载后比对 provider 声明的快照摘要 | 不需要服务端 revision ID；whole-file 可证明内容等于声明 | 必须先完整下载；只适用小文件；依赖摘要语义实测 | 受限采纳 |
| 只靠 mtime/size/TTL 的弱缓存 | 兼容性最高 | 可能返回旧内容，或在一段读取中跨版本 | 只能显式弱模式 |

### 9.6 建议的门槛

```text
StableObjectID
&& StrongContentVersion
&& ReliableRangeRead
&& (PerRequestExactRevisionRead
    or verified conditional equivalent
    or whole-object declared-digest verification)
```

只有对象身份、强内容版本、可靠 range 和至少一种能证明缓存资格的内容绑定方式同时满足，文件数据才默认进入跨进程/重启持久缓存。整对象摘要例外只允许 whole-file 条目。

### 9.7 WebDAV ETag

WebDAV 只能把强 `ContentVersion` 暴露为强 ETag。只有弱 metadata 指纹时，应返回弱 ETag，或不返回可用于内容校验的 ETag。否则 Finder 和 WebDAV 客户端可能依据不可靠的验证器复用旧内容。

### 9.8 当前 provider 如何适配

- PKU：优先把 AnyShare `rev` 映射为 ContentVersion，并保存 signed request 的 revision 绑定证据。
- Baidu：`mtime + size + MD5` 可以辅助检测，但 dlink 未绑定 revision；在证明 range 下载一致前，range 缓存保持关闭。若 metadata MD5 经实机验证可靠，小于整文件阈值的对象可以完整下载并与打开快照中的 MD5 比对，通过后作为整文件条目缓存；更大文件不能由这一例外获得 range 缓存。MD5 只用于兼容性与意外变化检测，不作为抗攻击承诺。当前观察中只有部分字段是合法 MD5，因此默认仍关闭。
- Local：默认绕过持久数据缓存；若未来需要，改用文件系统稳定 file ID 而不是相对路径。
- S3：使用 versionId（如果 bucket 开启版本）或可靠 ETag，并通过条件读取验证。
- Google Drive：普通二进制文件需要验证 md5/revision 与下载绑定；Drive 原生文档必须显式导出格式。
- OneDrive：优先验证 cTag/eTag 与条件 GET/invalidating revision 的行为。
- WebDAV：必须区分 strong ETag 和 weak ETag；服务器可能不尊重 Range 或条件请求。

### 9.9 SDK 演进路径

采用可选能力接口，避免维护一套平行的 provider v2 interface：

1. `Object` 增加 `ContentVersion` 和可选 `MetadataVersion`；旧 provider 的弱 `Version` 不会被推断为强内容版本。
2. 实现 `RevisionReader` 的 session 才算支持精确 revision 读取；没有接口时退回 current-revision-only，并禁用持久缓存。
3. factory 声明能力上限；session 探测后只能收窄。对象可以用空 ContentVersion、Kind 或显式 opt-out 退出持久化。
4. broker、FSKit wire object 和 WebDAV ETag 使用同一套字段，不能各自解释旧 `Version`。
5. conformance 是启用任何持久化能力的门槛，而不是运行期的乐观猜测。

这样旧 provider 可以继续工作，新能力按 session 和对象安全降级，不需要维护长期并行的两套 SDK。

## 10. 读取路径与缓存

### 10.1 当前读取路径

- metadata cache 当前固定 TTL 2 秒，合并并发 List 和 Stat；远程 provider 的这一默认值需要测量后按 provider 调整。
- 每个 open file handle 保留一个 4 MiB block，缩小成驱动读取的大区间。
- 同一 handle 的 `ReadAt` 串行；provider 每次收到调用方 context。
- 关闭 handle 后内存块丢弃。
- broker 数据面当前直接调用 provider；WebDAV 使用 metadata cache 包装的 session。
- 还没有跨进程共享的持久数据层。

跨进程读路径和未来缓存位置：

```text
Finder / Spotlight / QuickLook
   -> macOS kernel
   -> FSKit extension (sandbox, no credentials)
   -> authenticated UDS/XPC: NodeKey + revision + range
   -> fusedockd
        -> namespace resolver / provider session
        -> VFS metadata + read planning
        -> optional persistent block cache
        -> provider request bound to the same ContentVersion
   <- binary payload / structured error
```

Linux FUSE 和 WebDAV 复用 daemon 中的 namespace/VFS/cache，仅 surface 适配不同。目录 metadata 可以在 root/TTL 层复用，但不能跨过账户 binding。

### 10.2 持久块缓存的定位

目标是让 FSKit、WebDAV 和 CLI 的后续操作复用已经下载的只读范围，使缓存跨 daemon 重启保留。它不是同步缓存，不拥有用户数据的权威副本，也不合并本地与远端内容。

### 10.3 缓存键

建议块键：

```text
account ID
+ provider ID
+ object ID
+ content version
+ file size
+ aligned block offset/length
```

块键使用长度前缀编码后做 SHA-256；整数使用固定宽度大端编码。目录缓存使用：

```text
namespaceTag = SHA-256(
  "fusedock-namespace-v1" || NUL ||
  lengthPrefixed(accountID) ||
  lengthPrefixed(providerID)
)
```

缓存目录按 namespaceTag 隔离，便于统计和清理单账户。缓存根、文件名和 header 不包含可读账户名、远端文件名、对象路径、签名 URL 或 token。

### 10.4 存储布局

```text
blocks/v1/
  owner.lock
  tmp/
  entries/<namespaceTag>/<keyHash>.blk
```

每个块文件自描述：

```text
magic            4 bytes   "FDBK"
format version   2 bytes   uint16 = 1
header length    2 bytes   uint16 = 80
payload length   8 bytes   uint64
key hash        32 bytes   SHA-256
payload hash    32 bytes   SHA-256
payload         N bytes
```

默认根：

| 平台 | 路径 |
| --- | --- |
| macOS | `~/Library/Caches/fusedock/blocks/v1` |
| Linux | `$XDG_CACHE_HOME/fusedock/blocks/v1` 或 `~/.cache/fusedock/blocks/v1` |
| Windows | `%LOCALAPPDATA%\fusedock\cache\blocks\v1` |

Unix 目录 0700、文件 0600；Windows 需要受保护 ACL 和 reparse-point 拒绝后才能启用。

### 10.5 默认读取策略

| 选项 | 默认 | 含义 |
| --- | --- | --- |
| block-size | 4 MiB | 大文件对齐块 |
| whole-file | 16 MiB | 小文件整文件缓存上限 |
| read-ahead | 1 block | 初版不做多块预取 |
| fill concurrency | 4 | 全局并发远端填充上限 |
| cache limit | 10 GiB | 磁盘预算 |
| metadata TTL | 待测 | 按 provider/session 设置；远程默认值需根据 Finder relist 和限频数据确定 |

### 10.6 填充流程

```text
ReadAt
  -> 校验 offset/length 和对象 Size
  -> 计算 ContentVersion 对应的块键
  -> 查内存索引
       命中：读取本地块；进程内首次命中完整校验 payload hash
       未命中：加入或创建单块 flight
  -> 只拉取完整对齐范围
  -> 校验精确字节数、大小和版本
  -> 完整临时文件 + close + 原子 rename
  -> 返回请求字节
```

每个块最多一个飞行中的 fill。等待者可独立取消；最后一个 waiter 离开时可以取消无用填充，但只要还有 waiter，成功结果可以继续发布。短读、超长响应、版本不匹配和取消都不能留下合法名字的部分文件。第一版按顺序处理跨块读取；只有在测量证明瓶颈后才加入并行预取。

缓存是可丢弃数据，默认不需要每块 `fsync` 和目录 `fsync`。崩溃后 rename 可见但内容不完整的文件必须由 header/payload 校验识别并删除；需要更强磁盘顺序保证时提供保守 fsync 模式。每个 payload 在进程内首次命中完整校验一次并标记 verified，后续不会对每次小读重复哈希。

### 10.7 一致性、并发与失效

- 缓存命中要求调用方已经持有精确的 ObjectRevision。
- ContentVersion 改变生成新键；旧键由 LRU 淘汰。
- 元数据 TTL 内的旧快照允许用于只读读取，但不能把它标成最新版本；远程 TTL 按 provider 配置并支持显式 invalidate。
- 索引锁只保护 map/LRU/refcount/budget，不覆盖网络或长时间磁盘 I/O。
- 每个缓存根只有一个 owner 进程，通过 `owner.lock` 互斥。
- 块文件损坏、大小不符、symlink、reparse point 都视为 miss 或 corruption，删除后正常 refill。
- cache 写失败或磁盘满时，仍返回已经成功下载的远端字节。
- per-handle 内存块和持久缓存共享全局内存上限；不能用“每个 handle 一块”形成无界内存。
- macOS 合成 `.metadata_never_index`，WebDAV 拒绝 `Depth: infinity`；真实 FSKit 行为未验证前不能假定 Spotlight 会自动跳过。

### 10.8 淘汰与失败模式

- 使用总预算内的 LRU，计算 header 和 payload。
- active reader 的块 pin 住；Windows 延迟删除。
- 单账户暂不做严格公平，但有 per-account 统计；未来可加配额或权重。
- 块大于预算时直接绕过持久化。
- 启动扫描只用文件 stat 的大小和 mtime 在后台建立 budget/LRU，并删除孤儿 temp；header 与 payload 校验推迟到首次命中。扫描完成前保守地避免超额写入。
- 块路径采用 `entries/<namespaceTag>/<hash前两位>/<keyHash>.blk`，LRU 初版从文件 mtime 恢复并在命中时批量 touch。
- payload SHA-256 能检测损坏和意外篡改，不能抵御同 OS 用户主动伪造；若威胁模型包含后者，需要 Keychain 保存的 message authentication code（MAC）或加密。

### 10.9 当前缓存明确不包含

- 写 staging、离线修改、冲突合并。
- 跨进程共享，除未来单一 daemon 外。
- 跨账户内容去重。
- 每块加密或用户提供的 cache key。
- 弱版本 provider 的静默持久化。

## 11. 表面适配器

### 11.1 Linux FUSE

- 使用 go-fuse，只读模式。
- 将 provider List/Stat 映射为 lookup/getattr/readdir，将 open/read 映射为 `ReadAt`。
- 4 MiB provider range 读取目前由 VFS per-handle 实现。
- 节点表必须用完整 NodeKey 隔离；协议 nodeid、用户可见 `st_ino` 和目录项 inode 信息分别建模。不能把 bare ObjectID 的 hash 直接当作协议节点身份。FUSE 不需要跨 remount 的持久 st_ino，但同一挂载内必须稳定。
- 本机验收仍需要真实 `/dev/fuse` 和 FUSE 3。

### 11.2 macOS FSKit

- 使用 Swift FSKit extension，不安装 macFUSE 或 kernel extension。
- Go 核心通过 `c-archive` 暴露 C ABI，Swift 负责 FSVolume/FSItem 操作。
- 正常账户路径通过 broker capability 访问 provider，沙箱内不持有账户秘密；直接 credential-file bridge 仅作为开发回退保留。
- 当前写、创建、删除、rename 都返回 `EROFS`。
- 当前 FSItem identifier 只哈希 object ID，且声明 persistent object IDs。该声明暂时必须改为 `false`；只有 daemon 持久保存 NodeKey 到平台 ID 的映射并能从任意持久 ID 找回对象时，才能恢复。
- 当前 volume identifier 每次生成随机 UUID。计划用 UUIDv5 从 `AccountID + normalized scope` 确定性派生，并拒绝同一目标的重复挂载。

### 11.3 WebDAV

- 只读 DAV class 1。
- 支持 PROPFIND、GET、HEAD、Range；写操作返回 405。
- subtree scope 在启动时解析，不能越出所选根。
- ETag 目前来自 ID+Version 的哈希；正式实现应改用强 ContentVersion。
- 拒绝 `PROPFIND Depth: infinity`，避免 Finder/第三方客户端触发无界遍历。
- 仅 loopback，Basic/Bearer 二选一，严格 Origin 和 Host 检查。
- Finder 实测成功；固定端口可以避免重启后地址漂移。

### 11.4 Windows 与移动端

- Windows 计划通过 WinFsp/cgofuse，Go 核和 CLI 已可交叉编译，但尚无 native mount 实现。
- Android/iOS 不应模拟内核挂载；更合适的是复用 provider/VFS 和受控下载服务。
- 浏览器应用走 WebDAV 或未来本地控制 API；不能把 WebDAV 的 Basic 认证直接暴露到公网。

## 12. 未来写入模型

当前没有写路径。未来的基本顺序：

1. 选择 provider mutation capability，不能把只读对象误写。
2. 本地私有 staging 文件记录 expected ContentVersion/ID。
3. create、write、truncate 先在 staging 中完成。
4. `fsync` 或 close 时才向 provider 发布。
5. 远端 revision 不匹配返回 `ErrConflict`，上层映射为 `EBUSY`，不能覆盖新版。
6. 失败保留 staging 和 recovery journal，不能用成功响应伪装持久化。
7. 跨账户/跨 provider rename 返回 `EXDEV`。
8. 非幂等 mutation 不能无 idempotency key 自动重试。

写入前必须先完成 namespace、版本和缓存的身份基础；否则恢复日志和 inode 都会建立在可变路径上。

## 13. 安全与隐私

### 13.1 资产

- 密码、OAuth token、refresh token 和 provider session state。
- 账户列表、endpoint、username 和远端对象元数据。
- 下载的文件内容。
- FSKit/WebDAV capability 和本地服务控制权限。

### 13.2 主要控制

- 元数据文件 owner-only；秘密存储在 OS credential store。
- broker 只使用 loopback，一次性 bootstrap，session token 仅在内存。
- Host/Origin/Referer/query/JSON/body/header/range 都有校验和上限。
- provider 错误文本不会成为响应；签名 URL 不出现在日志。
- FSKit 和 WebDAV 都是只读。
- 账户 provider ID、对象 ID 和远端名称都当作不可信输入。
- Local provider 使用 `os.Root`，防止路径逃逸。

### 13.3 威胁评估摘要

| 威胁 | 当前结论 |
| --- | --- |
| 其他局域网主机访问 broker/WebDAV | 绑定 loopback；拒绝非预期 Host |
| 其他 OS 用户读取 capability/auth | 0600 + 私有目录；Windows 尚需 ACL 加固 |
| FSKit 读到密码/refresh token | broker 不提供秘密路由 |
| 浏览器跨站读取 WebDAV | Origin 白名单、Bearer/Basic 和 CORS 限制 |
| provider 返回恶意路径 | 单组件名称校验，不拼成远端 API 路径 |
| 签名 URL 泄漏 | 从错误和日志中剥离 |
| 区块缓存被另一账户复用 | AccountID + ProviderID + object + content version |
| 缓存损坏影响正确读取 | 检测后删除并 refill；写失败降级为 miss |
| 同用户抢占 broker claim | 当前同类进程仍有窗口；计划 XPC/UDS + peer identity |

### 13.4 隐私取舍

持久缓存默认保存明文 payload，依赖全盘加密保护静态数据。可选 per-cache encryption 尚未设计；如果加入，需同时考虑 key 生命周期、daemon 启动解锁和崩溃恢复。

## 14. 测试与发布门槛

### 14.1 已有验证

- Go unit/race 测试，覆盖 provider、VFS、broker、WebDAV、CLI、账户和本地 provider。
- provider conformance 基础测试：root、list、name/ID 唯一性、size、可选 range read。
- PKU/Baidu HTTP fixture；真实账号手工验收。
- `make check`：gofmt、vet、race test 和 Linux/macOS/Windows cross-build。
- FSKit 真实扩展、WebDAV native client 和真实 Finder 读取已做过验收。

### 14.2 对 namespace 的验收

- v1 配置原子迁移后，重启仍使用同一 AccountID。
- 同名重建有不同 ID，旧 capability 和缓存不可读。
- 两个账户返回相同 ObjectID 时，NodeKey、inode 和缓存目录不同。
- 单账户和聚合视图对同一对象给出相同 NodeKey。
- 路径改名不改变缓存键。
- FSKit 不声称超出 provider 实际能力的 persistent object ID。

### 14.3 对版本的验收

- 连续读取同一 ContentVersion 得到逐字节相同内容。
- 内容变化后读取旧快照返回 `ErrConflict`，不能返回新字节。
- 续签签名 URL、重定向、重试和条件 GET 翻页等每个远端请求都继续绑定同一 ContentVersion。
- 条件 GET 收到 200、错误 Content-Range 或不同 ETag 时返回 `ErrConflict`。
- 打开后内容变化时，不能在同一 handle 中混合两个版本。
- 空 ContentVersion 不使用持久缓存。
- 短读、超长响应、错误 total size 和越界均不发布缓存条目。
- 并发 read、取消、关闭与冲突不会导致死锁或数据残留。

### 14.4 对缓存的验收

- 冷读、热命中、重启后命中。
- 跨块和最后短块正确。
- 多 waiter 共用一个 fill；一个 waiter 取消不破坏其他 waiter。
- 临时文件中断、启动清理、权限错误和磁盘满。
- magic/header/payload 损坏、长度错误、symlink 和 reparse point。
- LRU、pin、预算、Windows deferred deletion。
- FSKit 和 WebDAV 对同一目标共享 warm block。
- 缓存失效时仍能完成正确远端读取。

### 14.5 发布前需要补充

- provider version/ID/range conformance 成为强制门槛。
- 性能基准：冷/热读取、小文件、大文件 range、并发、淘汰和内存增长。
- 账户迁移、cache migration 和 crash journal 测试。
- 原生 Linux FUSE、Windows WinFsp、macOS FSKit 的平台回归。
- 代码签名、notarization、安装器和自动更新。

## 15. 实施顺序

### 阶段 A：立即修正与身份基础

1. 立即撤回 FSKit persistent-ID 声明，加入全局内存预算、provider TTL 配置和元数据索引保护。
2. 账户 v2：AccountID、binding fingerprint、凭据项迁移、原子迁移和别名规则。
3. 用可选能力接口引入 ContentVersion 和逐请求 revision 绑定；没有能力的旧 provider 自动禁用持久缓存。
4. namespace resolver：从 MountTarget + path 得到 NodeKey + ObjectRevision。
5. FUSE/FSKit 的节点身份切换到完整 NodeKey，并验证 volume UUID、重复挂载和 extension 重启行为。

### 阶段 B：共享本地服务

6. `fusedockd` 管理 provider sessions、VFS、缓存和挂载生命周期。
7. 认证本地控制面采用 Unix socket/XPC 和 peer identity；FSKit extension 可重连，不以无认证 TCP 暴露账户操作。
8. 数据面使用原始二进制 body/frame；稳定 WebDAV 凭据、固定 loopback 端口、重启恢复和诊断。
9. 同一 cached VFS 同时服务 broker 和 WebDAV；重新登录不卸载即可恢复。

### 阶段 C：持久块缓存

10. 先建立 latency、Finder relist、QuickLook、range 吞吐和内存基线，确定块大小、预读和 TTL。
11. 独立 cache package，先用 local fixtures；加入 fan-out 目录、后台扫描、首命中 payload 校验和无 fsync 默认策略。
12. PKU 启用逐请求精确版本缓存；Baidu 只允许经实测的整文件摘要小文件路径。
13. 增加 info、clear、verify、prune 和指标。

### 阶段 D：多账户与 provider

14. 单账户 scoped mount 先稳定，再做 aggregate root。
15. WebDAV client provider、S3、OneDrive、Google Drive。
16. 评估 iCloud 的独立接入路径。

### 阶段 E：写入与发行

17. staging、conflict、recovery journal、write capability。
18. Windows、签名、安装器、自动更新、provider SDK 和 sidecar。

## 16. 已知风险和未决点

### 16.1 设计风险

- binding fingerprint 的字段集必须版本化并显式列举；漏掉身份字段会串数据，纳入无关 option 会让 Finder 收藏和持久映射无谓失效。
- 版本能力如果只在 provider/factory 声明，无法覆盖同协议不同服务器；建议 session 级探测。
- FSKit 持久对象 ID 的语义和 FUSE nodeid/st_ino 并不相同，需要平台 adapter 分别设计。
- 缓存清除、SSD 擦除和备份副本无法提供物理删除保证。
- 明文 payload cache 对同用户恶意进程没有保密性。

### 16.2 产品风险

- iCloud Drive 不像普通 REST 对象存储，可能无法直接套用 provider contract；不能提前承诺“全平台所有网盘”。
- Google Docs、Baidu 特殊对象和 OneDrive 共享项需要明确导出/快捷方式语义。
- macOS 扩展签名、重启、系统设置和 Finder 收藏需要生产级安装与升级设计。
- Android/iOS 没有桌面文件系统 ABI，需要应用内浏览或 File Provider/SAF 等平台方案。

### 16.3 审阅后决定

2026-10-26 的独立审阅已对原十项给出明确立场；完整响应见 [design-review-response.md](design-review-response.md)。当前决定是：

1. 拆分 `ContentVersion` 与可选 `MetadataVersion`。
2. range 持久缓存门槛是每个远端请求绑定版本；条件 GET 改用 `If-Match` 并通过实测。整文件允许与打开快照中的 provider 声明摘要比对，但不得推广到 range。
3. factory 声明上限，session 探测收窄，对象可退出。
4. binding fingerprint 存在 AccountID 旁边，不参与随机 ID 派生。
5. FUSE 用显式 node map；FSKit 暂时关闭 persistent IDs，之后要么持久映射要么保持关闭。
6. 单 daemon 单 cache owner；共享缓存需要独立 root。
7. 首版用文件系统目录作为索引，后台扫描；不上 bbolt。
8. owner-only 明文缓存可接受，逐账户可关闭；加密延后。
9. macOS 默认按账户挂载 volume；aggregate 先在 CLI/WebDAV；冲突直接拒绝。
10. 用显式 Kind/能力表示 Document/Shortcut/Export；未知大小对象不能伪装为普通文件。

## 17. 可参考的外部实践

这些实践不是项目当前实现，而是设计依据和对照：

- RFC 9110 的 strong/weak validator：强 ETag 表示 representation data 字节一致；可参考 <https://www.rfc-editor.org/rfc/rfc9110.html>。
- NFSv4 将稳定 file handle 与 change attribute 分开，支持稳定对象身份而不把版本混入句柄。RFC 8881: <https://www.rfc-editor.org/rfc/rfc8881.html>。
- FUSE kernel 文档区分协议 nodeid、目录 lookup 和用户态 inode 映射：<https://man7.org/linux/man-pages/man4/fuse.4.html>。
- Apple FSKit 把 FSItem identifier、volume capability 和读写操作分开；持久对象 ID 需要真正可找回的稳定身份：<https://developer.apple.com/documentation/fskit/>。
- S3 versionId、HTTP `If-Match`、对象存储不可变版本是持久缓存读取绑定的成熟参考：<https://docs.aws.amazon.com/AmazonS3/latest/userguide/Versioning.html>。
- Google Drive 文件 ID、checksum、revision 和共享盘语义必须按对象类型解释：<https://developers.google.com/drive/api/guides/about-files>。
- Microsoft Graph 的 driveItem eTag/cTag 和条件请求需要实测并区分内容与元数据：<https://learn.microsoft.com/graph/api/resources/driveitem>。
- rclone VFS cache 展示了 chunked local cache、VFS writeback 和离线读写取舍，但 FuseDock 不应直接复制其全部语义：<https://rclone.org/commands/rclone_mount/>。
- PKU 的 IAAA/AnyShare 流程参考了 Apache-2.0 的 `unofficialpku/disk`；OpenList 的 AGPL 内部驱动未直接复用。

## 18. 最终判断

当前设计适合继续投入，但不应把“已有 provider + FUSE/FSKit/WebDAV 可用”误认为生产级通用网盘基础设施已经完成。最成熟的层次是 provider 抽象、只读表面和凭据隔离；最不成熟的是持久身份、强版本读取、缓存一致性和跨进程生命周期。

如果独立审阅只能给一个结论，最需要验证的是：

> 一个 provider 是否能明确证明“给定 AccountID、ObjectID、ContentVersion 和范围，读取的是同一个对象版本，且不会因并发更新返回混合字节？”

如果这个问题没有答案，持久缓存就不应默认开启，FSKit 也不应声称持久对象 ID。若这个问题得到强保证，FuseDock 的现有分层和缓存设计可以作为通用基础设施的合理基础。
