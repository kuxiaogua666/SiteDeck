# MT7688 嵌入式设备部署参考

设备 IP、账号、身份和实际部署记录仅保存本地。本文以同类型硬件说明布局与构建方法。
设备：WIDORA-NEO / MediaTek MT7688，MIPS 24KEc，小端、软浮点，128MB 内存，Linux 3.18.29 / OpenWrt。

部署前核对设备的 SSH 账号与访问策略；不同固件的 Telnet 认证行为可能不同。
传输程序和注册凭据使用 SSH，不通过明文 Telnet 传输。旧 Dropbear 的兼容算法参数仅用于这一设备的单次连接，未修改本机全局 SSH 配置。

## 已部署内容

安装目录：`/lj/sitedeck`，权限 0700。

| 文件 | 用途 |
| --- | --- |
| `site-agent` | 启动器，自动从 RAM 缓存运行主程序，并加载固定的数据、配置路径 |
| `site-agent-core.gz` | 无 UI 的 Linux/mipsle 软浮点 Agent 0.4.0，压缩存放以节省闪存 |
| `site-agent-core.md5` | 缓存版本与解压完整性检查，不用作身份认证 |
| `site-agent.json` | 公网 Hub 和注册凭据，权限 0600，默认只开放 host 能力 |
| `ca-certificates.crt` | HTTPS 校验使用的 ISRG Root X1/X2 公共根证书，没有关闭证书校验 |
| `data/` | 本设备独立身份、证书私钥及访客凭据，不与其他设备共用 |
| `sitedeck.init` | OpenWrt/procd 服务脚本的安装源文件 |
| `release.json` | 构建身份与文件校验值 |

系统服务入口：`/etc/init.d/sitedeck`，已启用开机自启。
设备现有业务程序、网络地址、网关和 Telnet/SSH 配置未改动；未升级或烧写固件。

## 资源与构建

通用 Agent 不能直接放入该设备：部署前剩余闪存约 7.2MB。
本次单独构建无桌面、无 Hub、无 CLI 控制端入口的 Agent，原始可执行文件约 9.6MiB，压缩后约 3.2MiB。
启动时只解压一次到 `/tmp/sitedeck-校验值/`，约占 9.6MiB tmpfs；后续命令直接复用缓存，重启后自动重建。
部署后闪存仍余约 4MB，上传临时包已删除。

Agent 空闲及连接后的实测 VmRSS 约 11MB，会随负载变化。`ps` 中约 527MiB 的 VSZ 是虚拟地址空间，不是占用了这么多物理内存。
运行配置为 `GOMAXPROCS=1`、`GOGC=50`、`GOMEMLIMIT=32MiB`，procd 将优先级设为 nice 10。
`GOMEMLIMIT` 是 Go 的软目标，不是包含 tmpfs 缓存在内的硬内存上限。

工具链位于 `/opt/toolchain-mipsel_24kec+dsp_gcc-4.8-linaro_uClibc-0.9.33.2`，已用其 ELF 工具核对架构和 ABI。
Agent 主程序为静态纯 Go 构建，不依赖 uClibc 动态链接器，也没有在设备上安装编译器、Go、Node.js 或新的 TLS 服务。

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=mipsle GOMIPS=softfloat \
go build -tags headless -trimpath -ldflags='-s -w' \
  -o site-agent-core ./cmd/site-agent-headless
```

## 启停与配对

```sh
/etc/init.d/sitedeck start
/etc/init.d/sitedeck stop
/etc/init.d/sitedeck restart
ubus call service list '{"name":"sitedeck"}'
```

查看现场配对文本，无需导出文件：

```sh
/lj/sitedeck/site-agent pair --plain
```

访客凭据沿用默认 24 小时。实际到期时间请在本机设备信息中查看。
到期后在设备上轮换并获取新文本；运行中的 Agent 会检测凭据变更并撤销旧连接：

```sh
/lj/sitedeck/site-agent rotate
/etc/init.d/sitedeck restart
/lj/sitedeck/site-agent pair --plain
```

本机已经完成文字配对，配置保存在：

```text
sitedeck-dis/.local/embedded-pair.json
```

在本机运行：

```sh
cd ../sitedeck-dis
./bin/site-cli --profile .local/embedded-pair.json --transport relay doctor
./bin/site-cli --profile .local/embedded-pair.json --transport relay host shell
```

设备 ID 与主机名以本机 Agent 信息为准，不在公开文档中记录。
已实际验证公网 WSS、设备指纹和访客认证、Hub 加密中继、只读命令及真实 PTY；PTY 输出 `EMBEDDED_PTY_OK`。
该精简系统没有 `hostname` 命令，可用 `cat /proc/sys/kernel/hostname`；这不影响 Agent 的主机名读取。
没有打开或测试物理串口，没有运行现有的闸机或老化测试脚本。

如需升级，不要直接把约 12MB 的通用二进制复制进闪存。先停止此服务，采用相同的压缩包布局更新，保留 `data/`。

旧设备原有安装路径和服务无需为改名迁移；新版启动器从自身目录读取配置与身份，新安装默认使用上述 SiteDeck 布局。
