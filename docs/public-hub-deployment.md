# 公网 Hub 部署说明

本页仅包含通用步骤，真实服务器地址、SSH 账号、设备 ID 和证书记录保存在本地 `.local/`，不随源码提交。
示例 `hub.example.invalid` 必须替换为自己的服务器地址，不能直接用于连接。

## 本地私有设置

```sh
mkdir -p .local
chmod 700 .local
cp deploy/server-deploy.example.json .local/server-deploy.json
chmod 600 .local/server-deploy.json
```

编辑 `.local/server-deploy.json`：`host` 为自己的服务器 IP 或域名，其他字段分别指定 SSH 用户、远端安装目录、
本机管理密钥目录、注册凭据、SSH known-hosts 文件和可选的目标设备 ID。路径相对源码目录解析。
不要把 SSH 密码写进此文件；上传工具在交互终端中读取密码。

```sh
python3 scripts/upload_hub.py --download-admin --start-admin-tunnel
python3 scripts/upload_hub.py --verify
```

也可以用 `--host hub.example.invalid` 或 `SITEDECK_SERVER_HOST` 覆盖主机。
首次 SSH 连接应通过可信渠道核对服务器公钥，并放入配置指定的 known-hosts 文件；工具不会自动接受未知主机。

## 服务器模板

`deploy/server/nginx.conf` 中的 `__HUB_HOST__` 是模板占位符。在服务器上传目录中运行安装器时提供自己的主机：

```sh
python3 /root/sitedeck/incoming/RELEASE/install.py --host hub.example.invalid
```

安装器核对发布包校验值，渲染 Nginx 配置，并把主机保存到 `/root/sitedeck/secrets/server-host`（0600）。
后续证书发布与验证从该私有文件读取主机；不需要把真实地址写回仓库。直接复制未经渲染的 Nginx 模板不能完成部署。
证书需覆盖配置的 IP 或域名，并由客户端信任；该安装器不替代首次证书签发和系统依赖准备。

默认布局：

| 项目 | 路径或名称 |
| --- | --- |
| Hub 程序 | `/root/sitedeck/bin/site-hub` |
| 注册、管理密钥和服务器主机设置 | `/root/sitedeck/secrets/` |
| 设备、授权状态 | `/root/sitedeck/state/hub-state.json` |
| 网关配置 | `/root/sitedeck/nginx/nginx.conf` |
| TLS 证书及私钥 | `/root/sitedeck/tls/current/` |
| Certbot 配置、账户 | `/root/sitedeck/acme/` |
| 日志 | `/root/sitedeck/logs/` |
| Hub / 网关服务 | `sitedeck-hub.service` / `sitedeck-gateway.service` |
| 证书续期服务 / 定时器 | `sitedeck-cert-renew.service` / `sitedeck-cert-renew.timer` |

服务模板使用 `/root/sitedeck`，改变安装根目录时需同步修改这些模板。
Hub 使用专用账户，后台仅监听 `127.0.0.1:48765`；公网网关仅代理 TCP 48763 的 `/ws`。
公网访问 `/`、`/admin/`、`/healthz` 返回 404 是预期行为。

## Windows 现场设置

启用远程接入，Hub WebSocket 填 `wss://hub.example.invalid:48763/ws`，注册凭据使用自己生成的 `hub.key`。
开启需要的能力并保存、启动。授权过期时轮换秘钥并续期，将新的配对文本提供给连接端。
机器需要能访问该服务器的 TCP 48763，并保持正确时间。

## 私有管理后台

使用 SSH 本地转发访问：

```sh
ssh -N -L 127.0.0.1:48764:127.0.0.1:48765 root@hub.example.invalid
```

保持终端运行，浏览器打开 `http://127.0.0.1:48764/admin/`，选择私有目录中的 `hub-admin.key` 登录。
关闭本机隧道不会停止公网 Hub；隧道退出后需重新建立。

## 服务与证书检查

```sh
systemctl status sitedeck-hub sitedeck-gateway --no-pager
systemctl list-timers sitedeck-cert-renew.timer --no-pager
journalctl -u sitedeck-hub -n 50 --no-pager
systemctl restart sitedeck-hub.service sitedeck-gateway.service
systemctl start sitedeck-cert-renew.service
journalctl -u sitedeck-cert-renew.service -n 40 --no-pager
```

证书更新脚本检查主机、有效期、公私钥匹配，发布后通过真实 TLS 握手确认网关加载的序列号。
采用 HTTP-01 时，需保证签发和续期期间的 TCP 80 可用。证书实际有效期以本地证书内容为准，不在公开文档中记录部署实例信息。

`verify_public_hub.py` 会创建临时本机测试 Agent，核对 WSS、强制中继、端到端命令、错误访客凭据拒绝和管理隧道。
输出文件保存在私有 `stateDirectory/verification.json`，其中含真实地址和临时设备 ID，不能公开。
