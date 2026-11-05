# SiteDeck 命名与升级

从 0.5.1 开始，产品名统一为 **SiteDeck（现场工作台）**。
源码模块和源码目录使用 `sitedeck`，本机运行目录为 `sitedeck-dis`，发布包统一以 `sitedeck-` 开头。
界面、Windows 标题栏和可执行文件属性、Hub 控制台、日志导出名称使用 SiteDeck。

## 命令与配对

命令继续使用 `site-hub`、`site-agent`、`site-agent-ui` 和 `site-cli`，配置文件仍为 `site-agent.json`。
`SITEPAIR1.` 文本、证书指纹、设备 ID、访客凭据和 v1 通信格式保持兼容。
旧部署的 Agent 可以连接新版 CLI，新版 Agent 也能接受旧版 CLI；无需仅因改名重新配对。
认证实现中保留的旧字符串是 v1 协议固定值，不是界面品牌，不应直接批量替换。

## 用户数据目录

新安装默认使用：

| 系统 | 默认目录 |
| --- | --- |
| Windows | `%APPDATA%\SiteDeck\Agent` |
| Ubuntu / Linux | `~/.config/SiteDeck/Agent`，支持 `XDG_CONFIG_HOME` |
| macOS | 用户 Application Support 下的 `SiteDeck/Agent` |

如果之前使用 `OpenEndlesst/Agent`，并且新版目录尚无独立身份，会直接复用旧目录中的身份和进程锁。
不会复制身份、生成新设备 ID 或自动轮换密钥。保存了旧配置但尚未初始化身份的安装也会复用旧目录。
通过 `--data-dir` 或 `--state` 指定的路径继续原样使用。

本机改名后，实际目录为 `sitedeck/` 与 `sitedeck-dis/`；旧源码和运行路径可以保留为兼容链接，
避免历史命令、SSH 隧道引用失效。新操作和新文档使用新路径。

## 服务器与嵌入式部署

改名只更新本地源码、构建物和安装模板，不会自动重启或迁移线上 Hub、Windows 或嵌入式设备。
新服务器模板使用 `/root/sitedeck` 和 `sitedeck-*` 服务名。
安装器从上传包所在的 `incoming/` 上级识别实际根目录；检测到匹配的旧服务和已有 Hub 状态时，
保留原服务账号、服务 ID 和状态路径，渲染模板后沿用它们。
真实远端安装目录仍保存在被忽略的 `.local/server-deploy.json` 中，不必为产品改名修改现有服务器。

嵌入式启动器从自身目录加载配置、身份和压缩程序，旧安装位置可继续运行。
新安装示例使用 `/lj/sitedeck`，服务源为 `deploy/embedded/sitedeck.init`。

## 源码提交

真实地址和凭据继续只保存在忽略的本地文件中。提交前执行：

```sh
make privacy
make source-archive
```

干净源码包为 `.local/publication/sitedeck-source.tar.gz`。
Windows 程序仍使用原图标设计，图标资源和文件版本重新生成后发布。
