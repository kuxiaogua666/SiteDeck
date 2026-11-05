# 功能与验证状态

验证日期：2026-11-02。当前版本：0.5.1（SiteDeck）。

## 当前实现

| 能力 | 状态 | 已验证范围 |
| --- | --- | --- |
| 自建 Hub、Agent 注册、设备在线发现 | 已实现 | 实际独立进程启动和退出 |
| Hub Web 管理及独立管理密钥 | 已实现 | 登录、错误密钥拒绝、同源/CSRF 校验；静态页面内嵌二进制 |
| 在线/离线设备、名称修改和策略持久化 | 已实现 | 浏览器改名；Hub 重启及 Agent 重新登记后名称仍保留 |
| 凭据级限时授权、权限交集、授权撤销 | 已实现 | 实际授权文件 CLI 验证、未授权能力拒绝、关闭配对旁路、撤销后拒绝连接 |
| Web 连接管理 | 已实现 | 浏览器展示真实 P2P 路径及断开；授权到期自动关闭通道 |
| 访客秘钥、Agent 指纹固定、TLS exporter 绑定的挑战响应 | 已实现 | 错误秘钥/指纹拒绝；旧凭据撤销 |
| 访客有效期、现场撤销 | 已实现 | 修改到期时间关闭运行中的 Agent；轮换后可重新配对 |
| P2P DataChannel | 已实现 | 本机 host 候选；Hub 中继载荷为 0 |
| 自动回落 Hub 字节中继 | 已实现 | 故意丢弃 ICE offer 后仍可执行命令 |
| TURN | 已接入 Pion 配置 | 编译通过，真实 TURN 和公网 NAT 尚未实测 |
| 主机命令、cwd/env/argv、退出码、取消 | 已实现 | Linux 真进程；远程失败和客户端取消 |
| 主机 PTY | 已实现 | Linux 真 PTY；Windows 0.3 Agent 的真实 ConPTY/PowerShell 已远程验证；0.4 Windows 新入口尚待实机验收 |
| SSH 指纹扫描、强制指纹校验、密码/私钥、exec/PTY | 已实现 | 测试 SSH 服务的密码认证、exec、stderr、退出码和错误指纹；私钥和 SSH PTY 尚未实机验证 |
| 共享会话、只读成员、最后成员退出 | 已实现 | 多远程连接及同一通道的多个逻辑成员 |
| 串口枚举、参数、独占、收发、等待、命令、watch | 已实现 | Linux 虚拟串口；未测试真实 USB/COM 硬件 |
| 串口 RX/TX、代次游标、过滤、溢出 | 已实现 | 虚拟串口真实字节收发及超过 1 MiB 的溢出 |
| daemon、连接复用、认证 IPC、断线重连 | 已实现 | 真实 CLI 多进程；断线命令不会自动重放 |
| 长输出下载、大小和 SHA-256、原始非 UTF-8 字节 | 已实现 | 长输出端到端下载及字节保留 |
| 并发频道上限、请求上限、会话/内存限额、审计摘要 | 已实现 | 核心路径运行；未做大规模压测 |
| Agent 本机 GUI 与 CLI 共用运行层 | 已实现 | 登录/同源/CSRF、配置脱敏、权限开关、Shell PTY、虚拟串口、秘钥轮换、远程 CLI、进程互斥与 headless 配置复用 |
| Windows 独立窗口 | 已实现，待实机验收 | amd64/arm64 交叉编译；WebView2 原生窗口及浏览器回退尚未在真实 Windows 验证 |
| 多平台发布物 | 已实现 | 9 组平台、29 个（含 Windows GUI 入口） `CGO_ENABLED=0` 二进制交叉编译 |

## 尚未实现

这些内容仍属于原设计中的后续工作，当前版本没有声称完整覆盖全部设计文档：

- OIDC/账号登录、设备归属和解绑；每设备的注册授权管理。
- 复杂公网 NAT 与真实 TURN 验证、TURN 临时凭据签发，以及 TURN/月度流量配额计费。公网 WSS/中继已实际验证。
- 现场能力无中断热更新、Windows SCM 服务集成、系统托盘与 macOS 原生窗口/服务安装包。Agent 本地能力开关已提供 GUI，保存会重启服务。
- 独立 task 查询接口、自动会话恢复；当前长任务可使用持续存在的 shell 会话进行增量读取。
- Telnet、CH9329 HID、音视频、Matter、自更新和发布签名。
- 串口光标查询自动应答、硬件流控配置、真实驱动对串口写操作阻塞时的取消验证。
- 官方 Hub 或原 Terminal 的协议兼容。

## 验证命令

```sh
go test -race ./... -timeout 90s
go vet ./...
make build
python3 scripts/smoke.py
python3 scripts/ui_smoke.py
python3 scripts/easy_smoke.py
python3 scripts/build.py
```

集成测试使用实际 WebSocket Hub、Agent、TLS 和 RPC，没有替换业务处理器。P2P 测试使用真实本机 ICE/DataChannel。SSH 测试使用进程内 SSH 服务；串口测试使用操作系统 PTY 模拟现场设备，因此不把这些结果称为真实硬件兼容验证。

`scripts/smoke.py` 启动编译后的 Hub、Agent、CLI 和 daemon 为独立进程。测试使用临时凭据、临时端口和临时文件；结束时关闭它创建的进程。结果写入 `bin/smoke-result.json`，不输出凭据。

管理后台还通过真实浏览器检查了登录、重命名、创建限权授权、连接断开及撤销。授权文件格式和 CLI `--access-file` 路径通过独立进程验证；浏览器下载按钮触发成功，浏览器工具未捕获到对应下载事件，未检查用户下载目录。

跨平台产物的目标架构、文件大小和 SHA-256 记录在 `bin/manifest.json`。源码在本机编译，尚未在 Windows、macOS 或其他 CPU 的设备上运行，也没有因此宣称对应平台已完成验收。


Agent UI 新增 10 项独立进程检查（`bin/ui-smoke-result.json`），原有 16 项检查继续通过。
浏览器实际登录并保存本机模式配置、创建 Shell 终端，输入命令得到 `BROWSER_TERMINAL_OK` 输出。
Windows amd64 的 CLI 帮助及 UI 后台在独立 Wine 目录中运行，已验证身份初始化、API 登录、状态读取、正常退出；
这不是 Windows 原生窗口或真实硬件验收。详情见 [Agent 界面说明](agent-ui.md)。


## 0.4.0 简化部署

- 新增程序旁 `site-agent.json` 自动发现、首次命令行配置向导和设置持久化；不复制设备身份。
- 新增 `SITEPAIR1.` 配对文本、`site-agent pair --plain`、`site-cli pair` 导入、`--pair-text -` 直接连接，以及 UI 复制入口。
- 新增 8 项真实进程检查：配置自动加载、跨工作目录启动、首次初始化、文字直接连接、私有导入、不同设备身份、交互向导和后台凭据脱敏。
- 原有 16 项业务进程检查和 10 项 UI 进程检查继续通过；测试、race 检查与多平台编译在本机执行。
- 0.4.0 预配置发布包仅含 Agent、部署设置和说明；含注册凭据，作为私有安装包保存，未上传服务器。
- 旧 Windows 0.3 Agent 仍可使用；本次不自动替换或重启在线设备、服务器 Hub。

详见 [简化部署说明](easy-deployment.md)。

## 0.5 系列与 SiteDeck 命名

- 0.5.0 增加串口 HEX 收发、原始收发时间戳、计数、定时发送、日志导出，以及本机 UDP、TCP Client、TCP Server 调试助手。
- 网络测试覆盖二进制收发、双客户端广播、指定发送、断开与资源释放；串口时间戳按实际 I/O 保存，分段读取限制在通信帧大小内。
- 0.5.1 统一为 SiteDeck 品牌，保持命令名与 v1 认证和文字配对格式，旧身份目录兼容复用。
- 图形界面在 Linux 浏览器验证，Windows amd64/arm64 已交叉编译并检查图标资源；Windows 原生窗口外观仍需设备验收。
