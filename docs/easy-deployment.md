# SiteDeck 简化部署与文字配对

## 两个文件即可部署

将对应平台的 `site-agent`（Windows 为 `site-agent.exe`）和 `site-agent.json` 放在同一目录。
从任意工作目录启动都会优先读取程序旁的配置，无需每台机器输入 Hub 和注册凭据。
预配置 Agent 包已经包含这两个文件，没有放入 Hub、CLI、编译器或设备身份。

配置示例：

```json
{
  "hub": "wss://你的Hub地址/ws",
  "registrationToken": "hub.key文件的完整内容",
  "permissions": ["host"],
  "autoStart": true
}
```

可省略 `name`，每台设备会使用自己的主机名。默认访客有效期为 24 小时。
`permissions` 可选 `host`、`ssh`、`serial`；未指定时能力全部关闭。
私有预配置包默认只开启 `host`，需要 SSH 或串口时再编辑配置或通过 UI 勾选保存。
`autoStart` 控制打开 UI 时是否启动服务；显式 CLI 运行会直接启动。

预配置填写你自己的公网 Hub，例如 `wss://hub.example.invalid:48763/ws`，注册凭据读取你部署时生成的 `hub.key`。
包内配置包含注册凭据，只发给受信任的现场设备，不要放到公开下载站。

**同一配置可以复制给多台机器，不能复制设备身份目录。** 设备 ID、证书私钥和访客秘钥会在每台设备上单独生成。
Windows 升级时先退出旧 Agent；保留原用户数据目录即可继续使用原设备 ID。

## Windows 与无界面设备

- Windows 双击 `site-agent.exe`：打开 UI 并读取旁边的配置；预配置包会自动启动服务。
- Linux/macOS 执行 `./site-agent`：自动读取配置、初始化本机身份并启动，不用先执行 `init`。
- Windows Server 无 UI 时执行 `site-agent.exe --headless` 或 `site-agent.exe run`。
- 没有配置时，Windows 仍可在 UI 手动填写并保存；Linux/macOS 在交互终端中逐项提示并保存。
- 没有配置且没有交互终端时会提示配置方法，不会在服务进程中无限等待输入。

也可以显式生成配置，用于自动安装：

```sh
./site-agent setup --hub wss://你的Hub地址/ws --registration-file hub.key --allow host
./site-agent
```

`setup` 保存配置，不把设备私钥放入配置。已有普通 `run --hub ...` 等命令行参数仍作为本次运行的临时覆盖；
需要持久保存的命令行配置使用 `setup`。

## 不传文件，直接用配对文本

手动在终端启动 Agent 时，会显示 JSON 中的 `pairingText`，值以 `SITEPAIR1.` 开头。
复制这个完整值交给本机 CLI 或 AI 助手即可，包含 Hub、设备 ID、证书指纹、访客秘钥及有效期。
它是访问凭据的文本编码，不是一次性短码，也不是加密后的公开标识；有效期内应像配对文件一样保护。

后台服务默认不把配对文本写入日志。需要查看时在设备上执行：

```sh
./site-agent pair --plain
```

若指定过设备数据目录或配置文件，查看配对文本时也使用对应的 `--data-dir`、`--config` 或 `--state`。
自动部署需要在重定向的启动输出中获取文本时，可以明确执行 `site-agent run --print-pairing`。

Windows UI 的「设备与连接」新增「复制配对文本」，不再必须导出文件。旧的导出配对文件入口仍保留。

本机连接端执行：

```sh
./site-cli pair
```

按提示粘贴 `SITEPAIR1.` 开头的完整文本并回车，输入不回显，也不会进入 shell 命令历史。
CLI 自动在本机保存 0600 权限的 `.local/pair.json`，然后可以直接使用：

```sh
./site-cli doctor
./site-cli host shell
```

保存多个设备时：

```sh
./site-cli pair --output .local/device-a.json
./site-cli --profile .local/device-a.json host shell
```

同一设备重新配对会更新凭据；已有文件属于其他设备时默认拒绝覆盖，使用新路径或明确指定 `--force`。
`pair` 完成凭据导入，不会谎称设备已连接；运行 `doctor` 才验证实际连接。

自动化可以把配对文本通过标准输入交给 CLI，直接连接，不必保存文件：

```sh
./site-cli --pair-text - --transport relay doctor
```

也支持 `--pair-text '完整文本'`，但不建议将凭据直接写入命令行历史。
旧的 JSON 配对文件继续可用，`site-cli --profile 文件 pair-text` 可在本机将它转换为配对文本。
轮换秘钥后旧文本与旧配对文件一起失效，需要获取新的文本。Hub 如果要求独立访问授权，仍需按原规则提供对应授权。

## 配置查找与身份目录

配置优先级：显式 `--config` → 程序旁 `site-agent.json` → 设备数据目录中的 `site-agent.json` → 旧 `agent-ui.json`。
不从任意当前工作目录自动加载配置。UI 修改会保存回当前选中的配置文件；显式 `setup --config PATH` 可选择保存位置。
首次命令行向导默认保存到程序旁；没有写入权限时保存到用户数据目录，并显示实际路径。

用户数据目录沿用 0.3.0：Windows `%APPDATA%\SiteDeck\Agent`，
Linux `~/.config/SiteDeck/Agent`（支持 `XDG_CONFIG_HOME`），macOS 用户 Application Support。
没有用户配置目录的嵌入式环境回退到程序旁 `.site-agent/`。使用 `--data-dir` 可明确指定。
升级时会兼容读取旧 `OpenEndlesst/Agent` 目录中的身份与配置；不会仅为改名重新生成设备。

旧的 `--config agent-ui.json` 与 `--state` 路径用法保持兼容。原有远程 Windows 0.3.0、Hub 0.3.0 不要求为了新客户端而重启；
现场要使用自动配置和配对文本显示功能时，更新现场 Agent 到 0.4.0。

### CLI 连接受到本机代理影响

如果环境设置了 HTTP_PROXY/HTTPS_PROXY，CLI 默认使用环境代理。连接 Hub 返回 EOF 时，可显式直连（TLS 验证仍保持开启）：

```bash
./bin/site-cli --profile .local/windows-pair.json --proxy direct --transport relay host shell
```

也可以在本机配对 JSON 中加入 `"proxy": "direct"`，之后原命令即可使用。`--proxy env` 可临时恢复环境代理。更新同一设备的配对文本会保留此设置，配对文本自身不包含本机代理偏好。已运行的 daemon 需在启动时设置代理模式。
