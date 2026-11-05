# 独立协议 v1

本协议不兼容原 `endlesst.device/1`。Hub 只转发信令或加密字节，业务 RPC 不由 Hub 执行。

## 连接与身份

1. Agent 用受保护文件中的部署注册凭据建立 WSS，发送 `register`。
2. CLI 请求 `deviceId`；Hub 返回随机 `channelId`。每设备默认最多 8 个频道，最多可配置 64 个。
3. CLI 发起 ICE 或选择字节中继。每次协商使用独立 `attempt`，旧协商的答复和中继数据不会进入新的流。
4. 在可靠有序 DataChannel 或中继字节流上建立 TLS 1.3。CLI 校验配对时取得的 Agent 证书指纹。
5. Agent 发出一次性随机挑战。双方使用各自方向标签、设备 ID、频道 ID、随机挑战和本次 TLS exporter 计算 HMAC-SHA256。访客秘钥不发送到 Hub。
6. Agent 校验本地访客有效期、HMAC 和能力开关，随后接收 RPC。

Hub 管理签发的访问凭据通过 `connect.accessToken` 在 WSS 中提交，独立于上述现场访客秘钥。Hub 校验设备、权限、有效期和撤销状态，向 Agent 提供 Ed25519 签名的通道授权；其设备 ID、频道 ID 和过期时间均需匹配。签名公钥通过 Agent 已验证的 Hub 注册连接提供，Agent 再将授权能力与现场开关取交集。当前支持 `accessProtocol:1`，旧 Agent 不允许使用受限授权。

后台创建授权默认原子地将目标设备切换为仅授权访问，关闭仅配对访问旁路。显式设置 `requireGrant:false` 才会保留原配对模式。授权到期、撤销或设备阻止会同时关闭信令和业务通道，包括 P2P。

当前信令采用完整 SDP 收集后发送，不是 trickle ICE。心跳间隔 20 秒，Hub 对无心跳连接设置 60 秒读取期限。设备身份、账号归属和访客认证没有混为一项：当前只实现访客认证，没有 OIDC 账号归属。

默认 Hub 地址为 `127.0.0.1:48763`。浏览器管理位于 `/admin/`，Agent/CLI 协议仍使用 `/ws`。设备名称、访问策略和授权哈希持久化到 Hub 状态文件，在线状态则取自当前活动连接。

## 管理 API

| 接口 | 用途 |
| --- | --- |
| `POST /admin/api/login` | 用独立管理密钥建立 8 小时管理会话 |
| `GET /admin/api/session` | 查询会话与 CSRF token |
| `POST /admin/api/logout` | 注销管理会话 |
| `GET /admin/api/overview` | 设备、授权记录与当前连接；不返回签名私钥、授权原文或哈希 |
| `PATCH /admin/api/devices/{id}` | 修改 `name`、`allowPaired` 或 `blocked` |
| `POST /admin/api/grants` | 创建限时授权，原始 `accessToken` 只在该响应返回 |
| `DELETE /admin/api/grants/{id}` | 撤销授权并关闭其现有通道 |
| `DELETE /admin/api/connections/{id}` | 断开当前通道，不撤销授权 |

管理请求使用 HttpOnly/SameSite 会话 Cookie；修改接口同时需要 `X-CSRF-Token`。JSON 输入有大小限制并校验字段和同源，管理登录有限流。设备改名不改变设备身份或现场主机名。管理后台当前只有共享管理员密钥，不提供 OIDC 或多用户角色管理。

## RPC

TLS 内使用 UTF-8 NDJSON，请求示例：

```json
{"version":1,"id":"unique-request-id","clientId":"logical-client-id","method":"host.exec","params":{"argv":["hostname"]},"timeoutMs":10000}
```

成功或业务失败：

```json
{"version":1,"id":"unique-request-id","result":{"stdout":"SITE-PC\n","stderr":"","exitCode":0,"truncated":false,"timedOut":false}}
{"version":1,"id":"unique-request-id","error":{"code":"PERMISSION_DENIED","message":"现场未允许 host 能力","retryable":false}}
```

每通道最多同时执行 8 个请求；请求帧最多 128 KiB，CLI 参数体最多 96 KiB，Agent 的单请求期限最多 5 分钟。客户端超时发送 `request.cancel`，取消必须匹配同一通道和 `clientId`。不会因重连自动重放 RPC。

`clientId` 用于区分复用同一 daemon 通道的逻辑会话成员，不是独立用户认证。持有同一访客秘钥的用户属于同一个权限主体。

## 方法

| 方法 | 参数要点 | 结果 |
| --- | --- | --- |
| `doctor` | `{}` | 设备、版本、OS/架构、认证、有效期、实际传输和方法清单 |
| `permissions.get` | `{}` | 现场允许的能力 |
| `host.exec` | `command` 或 `argv`；可选 `cwd`、`env`、`shell` | stdout、stderr、exitCode、timedOut、截断和可选下载引用 |
| `host.shell` | 可选 `argv`/`command`、cwd/env/shell、rows/cols | 持续运行的终端会话 ID |
| `ssh.hostkey.scan` | host、可选 port | 未经信任验证的主机公钥指纹 |
| `ssh.exec` | host、port、user、fingerprint、password/privateKey、command | 命令结果 |
| `ssh.shell` | SSH 身份参数及 rows/cols | 持续运行的 SSH 终端会话 ID |
| `session.list` | `{}` | 当前允许查看的 Agent 会话 |
| `session.attach` | id、可选 readOnly | 将当前逻辑客户端加入会话 |
| `session.detach` | id | 移除当前成员；shell 最后一个成员离开后关闭 |
| `session.read` | id、可选 cursor/limit/waitMs | Base64 数据、游标、overflow、eof、退出码 |
| `session.write` | id、text 或 Base64 data | 需要先作为可输入成员加入 |
| `session.resize` | id、rows、cols | 调整 PTY 大小 |
| `session.close` | id | 关闭并移除会话 |
| `serial.list` | `{}` | 端口名称数组 |
| `serial.connect` | port、baudRate、dataBits、stopBits、parity | 独立持有的物理端口会话 |
| `serial.read` | id、cursor、limit、waitMs、source | RX/TX 事件与原始字节 |
| `serial.write` | id、text 或 Base64 data | 写入字节数 |
| `serial.wait` | id、pattern、可选 regex/cursor | 从指定位置或当前尾部等待新 RX |
| `serial.exec` | id、text、pattern、可选 regex | 发送前取游标，等待新 RX 中的匹配 |
| `serial.disconnect` | id | 关闭物理串口 |
| `artifact.read` | id、offset、limit | 分块 Base64 数据、nextOffset、eof |
| `request.cancel` | id | 尽力取消当前连接拥有的请求 |

CLI 的 `serial watch` 是持续调用 `serial.read` 的本机适配；`artifact download` 自动拉取分块。这两项不是独立的现场 RPC。

主机 shell：Linux/macOS 使用系统 PTY，Windows 使用 ConPTY。Windows 默认 PowerShell，也可明确选 `pwsh` 或 `cmd`。Windows ConPTY 尚未实机验证。`host.exec` 的 `argv` 无 shell 解释；`command` 则有明确的平台 shell 语义。

SSH `privateKey` 为 PEM 内容，可选 `passphrase`。扫描阶段收到主机公钥后即停止，不尝试登录。执行阶段强制匹配调用者提供的 SHA-256 主机指纹。

## 会话、游标与缓存

会话由 Agent 持有，单个 CLI 的退出不删除尚未加入成员的会话。先 `host.shell --json` 创建，再用另一个客户端加入是支持的。已加入的 shell 成员在通道断开时移除，最后一个成员移除会关闭 shell。串口需要显式断开，不跟随观察客户端退出。无人参与且 30 分钟无读取或输入的会话会被回收，Agent 最多保存 64 个会话。

每会话最多保留 1 MiB 原始字节。串口同时保留最多 4096 个合并后的方向区间；达到字节或区间上限会推进最早游标并报告溢出。游标由会话随机 ID 与绝对字节偏移编码，跨会话或重开后的旧游标被拒绝。

`serial.read` 默认 `source:all`，其 `data` 为筛选后的字节，`events` 包含方向、字节和对应游标。无论筛选方向如何，`nextCursor` 都在同一总时间线上推进。`serial.exec` 和 `serial.wait` 只匹配 RX，不会因为自身 TX 包含提示符而误报。执行中的串口命令持有独占操作锁，其他写操作返回 `RESOURCE_BUSY`，读取观察仍可继续。

原始字节使用 Base64，主机文本默认按 UTF-8 显示；无法有效解码时保留 `rawStdout`/`rawStderr` 原字节，显示文本中的替代字符不作为原始输出。串口不自动回应终端光标查询，也不修改原始输入输出。

超过内联阈值的主机/SSH 结果保留为有大小和 SHA-256 的 JSON artifact。CLI 自动下载只保存经过哈希核对的内容。Agent 的 artifact 缓存最多 16 MiB，达到总量上限或重启时可能清除旧缓存。

## 权限和错误

现场 `--allow` 固定当前进程的 host/ssh/serial 能力；关闭或改变权限需要现场重启 Agent，不提供远程提权 RPC。轮换或到期会撤销活动通道和现场资源。

主要错误码：`INVALID_PARAMS`、`AUTH_FAILED`（内部认证）、`CHANNEL_AUTH_FAILED`（CLI 建连认证阶段）、`DEVICE_OFFLINE`、`CHANNEL_QUOTA_EXCEEDED`、`PERMISSION_DENIED`、`RESOURCE_BUSY`、`INVALID_CURSOR`、`SESSION_NOT_FOUND`、`SESSION_CLOSED`、`SSH_CONNECT_FAILED`、`SERIAL_OPEN_FAILED`、`DEVICE_NOT_FOUND`、`TIMEOUT`、`CANCELED`、`TRANSPORT_CLOSED`、`ARTIFACT_NOT_FOUND`。

实际传输中断时，尚未确认的业务操作可能已经执行。错误返回说明不确定性，不承诺硬件写操作恰好执行一次。
