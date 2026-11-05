# SiteDeck Agent 界面与命令行

版本 0.5.1，2026-11-02。SiteDeck 是独立部署的现场工作台。

0.4.0 新增程序旁 `site-agent.json` 自动读取和「复制配对文本」。预配置包解压即可用，详细流程见 [简化部署](easy-deployment.md)。

## Windows 上打开界面

部署目录的 `platforms/windows-amd64/` 适用于普通 Intel/AMD Windows，
`platforms/windows-arm64/` 适用于 Windows ARM64。

- 双击 `site-agent-ui.exe`：打开独立窗口，不附带控制台。
- 双击 `site-agent.exe`：没有参数时同样打开界面，仅隐藏程序自己创建的控制台。
- PowerShell 中执行 `site-agent.exe ui`：打开同一界面。
- `site-agent.exe run/init/info/rotate`：仍然是原有的命令行入口。

窗口使用系统 Microsoft Edge WebView2 Runtime。缺少运行时或窗口启动失败时会显示提示，
并尝试使用默认浏览器打开相同界面；也可执行 `site-agent.exe ui --browser`。
独立窗口不需要安装 Go、Node.js、Electron 或编译器。交互式本机终端依赖 Windows ConPTY；
建议使用具备 ConPTY 和 WebView2 Runtime 的 Windows 10/11。

首次打开不会自动启动现场服务，现场能力默认关闭：

1. 打开「连接设置」，设置设备名称。
2. 远程使用时填写你自己 Hub 的可访问 `wss://域名/ws` 地址，并导入该 Hub 的 `hub.key`。
   同机测试可使用 `ws://127.0.0.1:48763/ws`。远程 Windows 上的 `127.0.0.1` 指向 Windows 自己。
3. 勾选实际需要的本机终端、SSH、串口能力，点击「保存并应用」。
4. 点击「启动服务」。远程模式中「Hub 已连接」表示已登记成功；「正在重连」时查看设备页记录。
5. 在「设备与连接」导出配对文件，供原有 `site-cli` 使用；Hub 的独立访问授权仍在 Hub 后台管理。

只使用本机终端、SSH、串口时，关闭「启用远程接入」，保存后启动服务即可，不要求部署 Hub。
远程模式下，Agent 所在机器不要求公网 IP，但必须能访问你部署的 Hub。
当前部署没有自动配置公网入口、隧道或 TURN 服务。

## 界面功能

- 本机 Shell、SSH、串口终端标签页；真实终端输入、输出和窗口大小同步。
- SSH 主机指纹读取与显式核对确认，密码或私钥连接；密码、私钥不写入设置和运行记录。
- 串口枚举、波特率、数据位、停止位、校验位；终端显示 RX，输入发送到串口。
- 加入已有会话、只读加入、关闭标签页或关闭共享会话。关闭共享会话会影响所有连接者；
  关闭串口标签页只退出观察，物理串口需在已有会话中显式关闭。
- 现场能力开关、服务启停、Hub 状态、远程连接、运行摘要记录。
- 配对文件导出、访问秘钥轮换和续期。轮换立即撤销原连接，旧配对文件失效。
- 运行中保存设置会重启 Agent 并关闭其活动资源。「下次打开界面时自动启动现场服务」
  仅控制打开本程序时的行为，不是开机自启或 Windows 系统服务。

关闭独立窗口会停止本次现场服务。浏览器模式下关闭网页不会停止后台进程，
需要使用「退出程序」或在启动终端中按 Ctrl+C。当前没有系统托盘、最小化后台保活或 SCM 安装功能。

## 数据目录、端口与诊断

默认 UI 数据目录：

| 系统 | 目录 |
| --- | --- |
| Windows | `%APPDATA%\SiteDeck\Agent` |
| Linux | `$XDG_CONFIG_HOME/SiteDeck/Agent`，未设置时为 `~/.config/SiteDeck/Agent` |
| macOS | `~/Library/Application Support/SiteDeck/Agent` |

这是新安装的默认持久目录，不受 EXE 所在目录或双击时工作目录影响。
升级时会兼容复用 `OpenEndlesst/Agent` 中的旧身份与配置；请以界面显示的数据目录为准，显式配置路径也使用该实际目录。
既有 CLI 的 `.local/agent.json` 不会自动导入；需要共享原身份时，将对应 `agent.json` 放入指定 UI 数据目录，
并确保原 Agent 已停止。不要同时复制运行同一身份。

`agent-ui.json` 保存界面设置和受保护的注册凭据，`agent.json` 保存现场身份；请勿把数据目录公开分享。
`native-ui.log` 记录 Windows 窗口错误。`WebView2/` 是窗口运行时的用户数据。
`ui-runtime.json`、`ui-access.key` 和 `.open-*.html` 是运行时文件，正常退出时会清理。

默认界面监听 `127.0.0.1:48767`；默认端口被占用时选择其他空闲回环端口。
显式设置的端口被占用则报错。只允许监听回环 IP，不能把本机控制界面暴露到公网。

```powershell
.\site-agent.exe ui --data-dir "D:\MyAgentData" --listen 127.0.0.1:48771
.\site-agent.exe ui --browser
.\site-agent.exe ui --no-open
```

`--no-open` 输出 URL 和启动文件路径，不输出秘钥。手动打开数据目录内 `.open-*.html` 可自动进入，
该文件只能使用一次，2 分钟后过期。直接打开 URL 时，需要导入或粘贴当前 `ui-access.key`。
界面凭据只保留在页面内存中，不使用跨端口共享的 localhost Cookie，也不保存到浏览器存储；
刷新页面后需要重新解锁。重新运行程序会激活已有原生窗口或打开同一个后台，不会启动重复身份。

## CLI 与界面共享配置

先在界面保存配置并退出，然后可直接无界面运行：

```powershell
.\site-agent.exe run --config "$env:APPDATA\SiteDeck\Agent\agent-ui.json"
.\site-agent.exe info --config "$env:APPDATA\SiteDeck\Agent\agent-ui.json"
```

`--config` 同时选择该配置目录中的 `agent.json`；明确传入的 `--hub`、`--allow`、`--state`、
`--registration-file`、`--ice-file` 等参数覆盖配置文件中的对应选项。原有不带 `--config` 的 CLI 用法不变。
运行中的同一身份带进程锁，不能再通过另一个 CLI 或 GUI 重复启动。`run --config` 不监视设置文件变更，
修改配置后需要重启命令行 Agent；访问秘钥变化仍会被监视并撤销旧连接。

Linux/macOS 使用 `site-agent ui` 打开浏览器界面，命令行入口保持不变。

## 验证范围

Linux：界面登录、配置保存、服务启停、真实 Shell 输入输出、会话大小调整、权限拒绝、
秘钥轮换、凭据脱敏、与现有远程客户端通信以及 UI/CLI 身份互斥均有实际运行验证。
串口通过 Linux PTY 模拟设备验证，未验证真实 USB/COM 硬件。

Windows amd64/arm64：纯 Go 交叉编译；amd64 CLI 和 UI HTTP 后台另在 Wine 下启动验证。
尚未在真实 Windows 上验收 WebView2 窗口、自动浏览器回退、ConPTY、SSH 窗口交互或硬件串口。
Wine 不等于真实 Windows 验证。其他平台也没有因交叉编译成功而被视为已完成实机验收。

前端使用公开发布的 xterm.js 6.0.0 与 addon-fit 0.11.0，本地内嵌，无 CDN 依赖。
原应用仅用于静态功能和结构参考，未复制原应用界面业务代码。

## 0.5.0：串口与网络调试助手

Windows 的 `site-agent.exe`、`site-agent-ui.exe`、`site-cli.exe`、`site-hub.exe` 均嵌入多尺寸应用图标和版本信息。Agent 窗口标题栏、任务栏与浏览器页面使用同一图标。图标位于可执行文件内部，无需额外文件；非 Windows 二进制不链接这些 Windows 资源。嵌入式 `-tags headless` 版本不包含 UI、图标和网络助手后端。

### 串口

在「终端 → ＋ 串口」配置端口和波特率后，默认打开串口助手。可以直接加入已有串口会话，不需重新占用 COM 口。

- HEX 显示：展示原始字节，包括 `00`、`FF` 和控制字符。切换显示方式不修改数据。
- HEX 发送：输入 `01 03 00 FF` 或连续字节 `010300FF`；格式不完整时不发送。
- 文本发送：UTF-8 编码，可选追加 CRLF、LF、CR 或不换行。
- 收发方向与毫秒时间戳：时间在 Agent 收发时记录，回看历史保留原时间；串口按读取片段记录，不自动识别业务协议帧。
- RX/TX 字节计数、自动滚动、清空显示、保存当前记录。
- 定时发送：50–3600000 ms，发送完成后等待指定间隔；出错、会话结束或关闭标签页会停止。浏览器计时不用于实时精密控制。
- 「切换到交互终端」保留原来的串口控制台。只读加入不允许任何发送。

每个串口后端最多保留 1 MiB 数据及 4096 个收发片段，读取按页返回。界面最多保留 512 KiB 原始数据或 2048 条记录，先到上限者淘汰；清空仅影响当前界面与计数，不清除共享会话历史。

### 网络

左侧「网络调试 → 新建网络会话」，支持三种模式：

| 模式 | 用法 | 默认本地地址 |
| --- | --- | --- |
| UDP | 监听并收发数据报；发送目标可随时修改 | `127.0.0.1:48772` |
| TCP Client | 连接 IP／主机名和端口，双向收发 | 系统分配 |
| TCP Server | 监听，显示客户端列表，选择客户端发送或广播，手动断开客户端 | `127.0.0.1:48771` |

需要其他设备连接本机时，本地地址填写网卡 IP 或 `0.0.0.0`，并在系统防火墙中允许对应端口。IPv6 地址使用 `[::1]:48771` 格式；本地端口 `0` 表示系统自动分配。网络助手与串口助手共用 HEX／文本、时间戳、计数、记录保存与定时发送操作。

网络助手在 UI 所在设备直接创建套接字，无需启动现场服务或连接 Hub。它是本机界面工具，不注册远程 RPC；远程 CLI 不能调用这些本机网络接口。每个界面最多 12 个网络会话、每个 TCP Server 最多 32 个客户端；单次发送最多 32 KiB。TCP 是字节流，收发记录的片段不代表应用协议包边界。UDP 保留接收数据报边界。

每会话后台记录限制为 1 MiB 或 1024 条。关闭网络标签页立即关闭对应连接；退出 Agent 关闭全部连接；界面授权闲置过期后清理其连接。关闭浏览器页面时请先关闭网络标签页，避免等待闲置清理。

### 重新生成 Windows 图标资源

修改 `internal/agentui/web/icon.svg` 后，将其转换为 `deploy/icons/app.ico`，包含 16、24、32、48、64、128、256 像素尺寸，然后运行：

```sh
python3 scripts/windows_resources.py
python3 scripts/build.py --targets windows/amd64,windows/arm64
```

资源生成器固定使用 `go-winres v0.3.3`，仅用于构建，不增加运行时依赖。生成的 `rsrc_windows_amd64.syso` 和 `rsrc_windows_arm64.syso` 按平台自动选用，嵌入式部署仍只需 headless 程序和原有配置。
