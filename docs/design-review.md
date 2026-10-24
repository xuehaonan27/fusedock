# FuseDock 设计审阅总稿

| 文档日期 | 2026-10-24 |
| --- | --- |
| 对应代码快照 | `620a358cb4ae7116fac966962e8679f446403075` |
| 审阅目的 | 让没有项目上下文的独立审阅者评估 FuseDock 的整体设计，重点判断 namespace、版本语义、持久缓存和跨平台挂载边界是否足以支撑后续实现。 |

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

当前这一模型存在一个必须解决的迁移问题：账户名可变，但凭据项以账户名为 key。重命名账户若要保留凭据，必须原子迁移凭据项，或者拒绝重命名。

### 7.2 建议中的 AccountID

每个账户增加不可变的 `acct_` + 128-bit 随机 ID。ID 不由名称、provider、username、endpoint 或密码哈希派生。账户绑定至少包括：

```text
provider ID + normalized endpoint + remote principal + provider options
```

- 修改显示别名、默认账户、密码或 token，不改变 AccountID。
- 修改 provider、endpoint、username、远端主体或身份相关 options，必须轮换 AccountID。
- 删除后同名重建必须获得新 ID。
- 无法获取稳定远端主体时，可以退回 username，但要明确降低身份保证。
- OAuth provider 更适合把 issuer + openid/subject 作为远端主体。
- AccountID 只是本地 namespace 标识，不是认证能力；知道它不能访问任何账户。
- CLI 的 JSON 输出和诊断应暴露 AccountID，自动化需要跨改名保持稳定时应使用它。

具体 binding fingerprint 只用于判断轮换，不应保存密码或可逆秘密。

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

建议：

1. FUSE 维护自己的 node map，区分协议 nodeid 和用户看到的 `st_ino`。
2. FSKit 的 FSItem identifier 从完整 NodeKey 稳定映射。
3. 映射表检测 64-bit hash collision，不能把两个节点当作同一个。
4. 在 StableObjectID 和持久找回能力完成前，把 FSKit 的 `supportsPersistentObjectIDs` 降为动态能力或 `false`。

### 8.6 迁移与轮换

账户配置需要从当前 v1 原子迁移：

1. 获取账户仓排他锁。
2. 严格读取旧配置，不生成临时 ID。
3. 校验新别名规则和大小写冲突。
4. 每个账户生成随机 ID 并检查碰撞。
5. 写临时文件、`fsync`、原子 rename、同步目录。
6. 只有新配置持久化成功后才释放锁；失败必须保持旧配置可读。

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
ContentVersion 非空且相同
=> ObjectID、Size、对应字节内容完全相同

内容字节发生变化
=> ContentVersion 必须变化
```

不需要规定版本递增、可比较或可解析。不同 ContentVersion 可以对应相同内容，最坏结果只是缓存未命中。

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
| Conditional GET | 服务端用 If-Match/If-Range 原子拒绝不匹配版本 | 通过实测后可 |
| Response version check | 响应头版本必须等于请求版本 | 通过实测后可 |
| Stat before/after read | 只能缩小竞态窗口，无法证明全程未变 | 不建议 |
| Current revision only | 始终读最新内容 | 禁止 |

`OpenRead(snapshot)` 应该接收 `ObjectID + ContentVersion + Size` 的版本快照。它要么返回该版本，要么返回 `ErrConflict`；不能在内部悄悄重新 Stat 并切换到新版本。若上层希望读新版本，只能创建新的 snapshot 后重试。

### 9.5 方案比较

| 方案 | 优点 | 缺点 | 结论 |
| --- | --- | --- | --- |
| 保持单一 Version，只加强注释 | SDK 改动最小 | 无法表达强弱保证，弱 provider 容易被误用；metadata 和 content 变化混在一起 | 仅适合过渡 |
| 单一 Version，但强制所有 provider 都提供强语义 | 模型最简单 | mtime/size 型 provider 无法合规；metadata 变化会误伤内容缓存 | 可作为 provider v2 的兼容层 |
| 拆分 ContentVersion/MetadataVersion，并新增读取绑定能力 | 语义清晰，可按 provider/对象降级；适合写入冲突 | 需要 provider contract v2、协议升级和 conformance | 推荐 |
| 用摘要直接充当版本 | 内容相等判断直接，可辅助完整性 | 多数服务不提供摘要；自己算需先下载；MD5 有碰撞问题；仍不能读旧版本 | 辅助，不作主键 |
| 只靠 mtime/size/TTL 的弱缓存 | 兼容性最高 | 可能返回旧内容，或在一段读取中跨版本 | 只能显式弱模式 |

### 9.6 建议的门槛

```text
StableObjectID
&& StrongContentVersion
&& ExactRevisionRead (or verified conditional equivalent)
&& ReliableRangeRead
```

只有四项同时满足，文件数据才默认进入跨进程/重启持久缓存。

### 9.7 WebDAV ETag

WebDAV 只能把强 `ContentVersion` 暴露为强 ETag。只有弱 metadata 指纹时，应返回弱 ETag，或不返回可用于内容校验的 ETag。否则 Finder 和 WebDAV 客户端可能依据不可靠的验证器复用旧内容。

### 9.8 当前 provider 如何适配

- PKU：优先把 AnyShare `rev` 映射为 ContentVersion，并保存 signed request 的 revision 绑定证据。
- Baidu：`mtime + size + MD5` 可以辅助检测，但 dlink 未绑定 revision；在证明范围下载一致前，只能允许内存缓存。
- Local：默认绕过持久数据缓存；若未来需要，改用文件系统稳定 file ID 而不是相对路径。
- S3：使用 versionId（如果 bucket 开启版本）或可靠 ETag，并通过条件读取验证。
- Google Drive：普通二进制文件需要验证 md5/revision 与下载绑定；Drive 原生文档必须显式导出格式。
- OneDrive：优先验证 cTag/eTag 与条件 GET/invalidating revision 的行为。
- WebDAV：必须区分 strong ETag 和 weak ETag；服务器可能不尊重 Range 或条件请求。

### 9.9 SDK 迁移路径

建议采用显式的 provider contract v2，而不是让新代码猜测旧 `Version` 的强度：

1. v2 对象增加 `ContentVersion`、`MetadataVersion` 和读取绑定能力。
2. 旧 v1 adapter 的 `Version` 只能作为 metadata hint；迁移层必须把 ContentVersion 留空，并禁用持久缓存。
3. 完成强语义和 conformance 的 provider 才升级到 v2 的持久缓存能力。
4. broker、FSKit wire object 和 WebDAV ETag 都使用同一套版本字段，不能各自解释 `Version`。
5. SDK 保留一段兼容窗口，但任何兼容路径都不能从旧字段推断强内容版本。

这是“一次清晰升级”与“渐进兼容”的折中：文件系统前端可以先兼容旧 provider，同时不会把旧数据误写进新缓存。

## 10. 读取路径与缓存

### 10.1 当前读取路径

- metadata cache 默认 TTL 2 秒，合并并发 List 和 Stat。
- 每个 open file handle 保留一个 4 MiB block，缩小成驱动读取的大区间。
- 同一 handle 的 `ReadAt` 串行；provider 每次收到调用方 context。
- 关闭 handle 后内存块丢弃。
- broker 数据面当前直接调用 provider；WebDAV 使用 metadata cache 包装的 session。
- 还没有跨进程共享的持久数据层。

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
| metadata TTL | 2 s | 元数据新鲜度窗口 |

### 10.6 填充流程

```text
ReadAt
  -> 校验 offset/length 和对象 Size
  -> 计算 ContentVersion 对应的块键
  -> 查内存索引
       命中：读取本地块，必要时校验
       未命中：加入或创建单块 flight
  -> 只拉取完整对齐范围
  -> 校验精确字节数、大小和版本
  -> 临时文件 + fsync + rename + 目录 fsync
  -> 返回请求字节
```

每个块最多一个飞行中的 fill。等待者可独立取消；最后一个 waiter 离开时可以取消无用填充，但只要还有 waiter，成功结果可以继续发布。短读、超长响应、版本不匹配和取消都不能留下合法名字的部分文件。第一版按顺序处理跨块读取；只有在测量证明瓶颈后才加入并行预取。

### 10.7 一致性、并发与失效

- 缓存命中要求调用方已经持有精确的 ObjectRevision。
- ContentVersion 改变生成新键；旧键由 LRU 淘汰。
- 元数据 TTL 内的旧快照允许用于只读读取，但不能把它标成最新版本。
- 索引锁只保护 map/LRU/refcount/budget，不覆盖网络或长时间磁盘 I/O。
- 每个缓存根只有一个 owner 进程，通过 `owner.lock` 互斥。
- 块文件损坏、大小不符、symlink、reparse point 都视为 miss 或 corruption，删除后正常 refill。
- cache 写失败或磁盘满时，仍返回已经成功下载的远端字节。

### 10.8 淘汰与失败模式

- 使用总预算内的 LRU，计算 header 和 payload。
- active reader 的块 pin 住；Windows 延迟删除。
- 单账户暂不做严格公平，但有 per-account 统计；未来可加配额或权重。
- 块大于预算时直接绕过持久化。
- 启动扫描 entries，删除孤儿 temp、非法 header 和大小不符文件。
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
- 根节点和普通节点的 inode 需要切换到完整 NodeKey 派生；协议 nodeid、st_ino 和目录项的 inode 信息要分别处理。
- 本机验收仍需要真实 `/dev/fuse` 和 FUSE 3。

### 11.2 macOS FSKit

- 使用 Swift FSKit extension，不安装 macFUSE 或 kernel extension。
- Go 核心通过 `c-archive` 暴露 C ABI，Swift 负责 FSVolume/FSItem 操作。
- 正常账户路径通过 broker capability 访问 provider，沙箱内不持有账户秘密；直接 credential-file bridge 仅作为开发回退保留。
- 当前写、创建、删除、rename 都返回 `EROFS`。
- 当前 FSItem identifier 只哈希 object ID，且声明 persistent object IDs；这一点必须在多账户或持久 inode 前修复。
- 当前 volume identifier 每次生成随机 UUID，需要评估 Finder 收藏、重新挂载和 volume 持久身份的关系。

### 11.3 WebDAV

- 只读 DAV class 1。
- 支持 PROPFIND、GET、HEAD、Range；写操作返回 405。
- subtree scope 在启动时解析，不能越出所选根。
- ETag 目前来自 ID+Version 的哈希；正式实现应改用强 ContentVersion。
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

### 阶段 A：身份基础

1. 账户 v2：AccountID、原子迁移、轮换和别名规则。
2. provider v2：StableObjectID、ContentVersion、MetadataVersion 和读取绑定能力。
3. namespace resolver：从 MountTarget + path 得到 NodeKey + ObjectRevision。
4. FUSE/FSKit 的节点身份切换到完整 NodeKey，并修正 persistent-ID 声明。

### 阶段 B：共享本地服务

5. `fusedockd` 管理 provider sessions、VFS、缓存和挂载生命周期。
6. 认证本地控制面采用 Unix socket/XPC，不以无认证 TCP 暴露账户操作。
7. 稳定 WebDAV 凭据、固定 loopback 端口、重启恢复和诊断。
8. 同一 cached VFS 同时服务 broker 和 WebDAV。

### 阶段 C：持久块缓存

9. 独立 cache package，先用 local fixtures。
10. 原子块发布、启动扫描、LRU、损坏恢复。
11. PKU 启用精确版本缓存；Baidu 等完成 revision 绑定后再启用。
12. 增加 info、clear、verify、prune 和指标。

### 阶段 D：多账户与 provider

13. 单账户 scoped mount 先稳定，再做 aggregate root。
14. WebDAV client provider、S3、OneDrive、Google Drive。
15. 评估 iCloud 的独立接入路径。

### 阶段 E：写入与发行

16. staging、conflict、recovery journal、write capability。
17. Windows、签名、安装器、自动更新、provider SDK 和 sidecar。

## 16. 已知风险和未决点

### 16.1 设计风险

- AccountID 绑定 fingerprint 仍可能过宽或过窄；错误的轮换只会冷缓存，错误的不轮换会串数据，因此当前倾向保守轮换。
- 版本能力如果只在 provider/factory 声明，无法覆盖同协议不同服务器；建议 session 级探测。
- FSKit 持久对象 ID 的语义和 FUSE nodeid/st_ino 并不相同，需要平台 adapter 分别设计。
- 缓存清除、SSD 擦除和备份副本无法提供物理删除保证。
- 明文 payload cache 对同用户恶意进程没有保密性。

### 16.2 产品风险

- iCloud Drive 不像普通 REST 对象存储，可能无法直接套用 provider contract；不能提前承诺“全平台所有网盘”。
- Google Docs、Baidu 特殊对象和 OneDrive 共享项需要明确导出/快捷方式语义。
- macOS 扩展签名、重启、系统设置和 Finder 收藏需要生产级安装与升级设计。
- Android/iOS 没有桌面文件系统 ABI，需要应用内浏览或 File Provider/SAF 等平台方案。

### 16.3 需要独立审阅拍板的事项

1. 是否正式采纳 `ContentVersion + MetadataVersion` 拆分，而不是保留单一 Version。
2. 是否把“精确 revision 读取”作为持久缓存硬门槛，还是允许经过验证的条件 GET 模式。
3. provider 能力应该属于 factory、session 还是每个对象。
4. AccountID 是否同时包含不可逆 binding fingerprint 以做防御性校验。
5. FUSE/FSKit 的 64-bit identifier 映射应采用哪种 hash/持久映射，如何证明 collision handling 可测试。
6. daemon 是否成为唯一 cache owner，是否允许未来多个 daemon 共享缓存。
7. 缓存采用启动扫描还是 bbolt/自定义索引；启动时间和百万小文件的权衡。
8. owner-only 明文缓存是否符合目标用户的威胁模型，是否需要 per-cache encryption。
9. 单账户和 aggregate view 的默认 UX，以及别名、Unicode 冲突的处理方式。
10. iCloud、Google Docs、Baidu 特殊文件是否需要独立的 provider subtypes，而不是强行映射为普通文件。

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
