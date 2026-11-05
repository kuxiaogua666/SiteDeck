# 提交源码与私有部署信息分离

应提交 `sitedeck/` 源码目录，运行目录 `sitedeck-dis/` 不属于源码。
运行目录包含真实 Hub 设置、注册密钥、设备配对文件及预配置压缩包，不应上传。
上级工作目录的忽略规则也排除了 `sitedeck-dis/`。

## 哪些文件保留本机

项目 `.gitignore` 排除了 `.local/`、身份与配对文件、密钥、实际 `site-agent.json`、日志、
预配置包、构建产物、原始研究安装包及完整 evidence。文档和脚本中的真实部署地址已经改成示例或私有配置读取。
`deploy/site-agent.example.json` 与 `deploy/server-deploy.example.json` 是公开模板，可以提交。
Windows 的 `cmd/*/rsrc_windows_*.syso` 与 `deploy/icons/app.ico` 也是公开构建资源，需要保留。

真实服务器选择保存到 `.local/server-deploy.json`，参照 `deploy/server-deploy.example.json` 创建。
实际 Agent 配置继续留在运行目录，不需要为了提交源码修改服务器、重新配对或重启设备。
清理前的原始文档与脚本可在本机 `.local/publish-backup/` 找到；不要上传该备份。

## 提交前检查

```sh
make privacy
```

没有初始化 Git 时，检查器使用临时 Git 索引应用本项目忽略规则，不会初始化或修改源码目录的 Git 仓库。
初始化仓库之后，可安装提交钩子：

```sh
git init
make install-hooks
make privacy
git add .
git diff --cached --stat
```

`.githooks/pre-commit` 检查将要提交的暂存区内容，能发现被 `git add -f` 强行加入的私密文件，
也能发现只在工作区删除、暂存区仍然保留的配对凭据。钩子依赖 Python 3 和 Git，不访问服务器。
它不会自动提交或推送。

检查器拒绝真实公网 IPv4 字面量、完整配对文本、私钥、明显的 JSON 凭据和私密文件路径。
示例地址使用 `example.invalid` 或 `192.0.2.0/24`，回环地址可以保留。
可在本机 `.local/publish-private-patterns.json` 中保存额外禁止公开的字符串数组；该文件本身被忽略。
新增域名、局域网地址、主机名或其他私有标识时，将其加入本地数组可避免误写回文档。

自动检查不能识别所有语境下的隐私，提交前仍应查看 `git diff --cached`。
忽略规则不会撤回已经提交的历史内容；如迁移已有仓库，需要另行检查其历史。

## 只导出源码

```sh
make source-archive
```

生成 `.local/publication/sitedeck-source.tar.gz`，只包含检查通过的可提交文件，不包含 `.git/`、
运行配置、私有备份、证据或预配置包。可以把该源码包解压到干净目录后初始化远程仓库。
离线回归检查命令：

```sh
python3 scripts/test_publication.py
```
