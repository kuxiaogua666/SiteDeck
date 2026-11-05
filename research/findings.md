# REA 分析与实现依据

分析时间：2026-10-22 至 2026-10-23。研究对象是用户已有的 Endless Terminal/CLI 3.1.24 安装包；没有连接官方 Hub，没有使用或提取用户账户凭据。

## 实际调用的工具

使用本机下载的 `../rea`，包版本 `6.7.0`。已执行锁定依赖安装。默认编译脚本因缺少可选的 `@tsrs/linux-x64-gnu` 失败，随后使用该仓库已安装的 TypeScript 编译器完成同一源码的编译、catalog 记录和运行时打包：

```sh
cd ../rea
npm ci --ignore-scripts --no-audit --no-fund
node node_modules/typescript/lib/tsc.js -p tsconfig.build.json
node scripts/record-catalog-digests.mjs
node scripts/record-mcp-catalog.mjs
node scripts/bundle-runtime.mjs
```

没有修改 REA 源码，没有执行 `rea setup` 或改变用户的 MCP/智能体配置；本次实际分析通过 REA CLI 调用。

## 原始对象与 Evidence

| 对象 | 身份与结果 |
| --- | --- |
| Linux CLI `endlesst-cli-3.1.24-linux-x64` | 原文件 SHA-256 `d2d0389c7613f9189f37d703c4894f8cf05e2498e005620ac3f87340ce3068d6` |
| CLI 内嵌脚本 | ELF `NODE_SEA_BLOB`，源文件偏移 `124846581`，长度 `546129` 字节；脚本 SHA-256 `60a39a20a9a2e1f8557d49d4127aa479e99b16a1105e77c89f69ddeb043366c2` |
| CLI REA Evidence | `ev_c3db9f0a15b8525a649672bcfa7144db80afec247b74d848188622941eaefba4`；本机 `research/evidence/cli-3.1.24.json` |
| Windows 安装包 | 7-Zip 识别为 NSIS；依次解出 `$PLUGINSDIR/app-64.7z` 和 `resources/app.asar` |
| Terminal `app.asar` | SHA-256 `00197f1055dc2e654b357cfd4de327d64eb4fc47c234e5b66a4b15886bb3394c` |
| 选定 Terminal 模块的 REA Evidence | `ev_ef72354b9f8e038f8bbaa1a63bd3910a25ae7b0a8a1d4a29ce9de8b95f4a3615`；本机 `research/evidence/terminal-selected.bundle.json` |
| 选定模块摘要 | `research/evidence/terminal-selected.summary.json`，其 summary Evidence 为 `ev_5289ef9fb850e3485c6a3248c1a01db934bb31786714028d56401e390ba0bc24` |

CLI 提取过程由 `extract_sea.py` 保存原文件哈希、脚本偏移、长度和脚本哈希，不运行目标程序。复现命令需要一个尚不存在的输出文件：

```sh
python3 research/extract_sea.py \
  ../endlesst/endlesst-cli-3.1.24-linux-x64 \
  research/artifacts/cli-3.1.24/main.cjs

node ../rea/scripts/rea.mjs analyze-javascript-application \
  "$PWD/research/artifacts/cli-3.1.24" --json \
  > research/evidence/cli-3.1.24.json
```

Terminal 的选定模块可以再次分析到新文件：

```sh
python3 research/analyze_terminal.py \
  --output research/evidence/terminal-new.bundle.json
```

## 静态观察与独立实现选择

下列文件路径均位于交付安装包 `app.asar` 中；便于查看的副本位于本机 `research/artifacts/terminal-selected/`。它们是随程序交付的编译后 JavaScript，不代表取得了原源码仓库、完整构建链或官方 Hub 源码。

| 观察 | 对应位置 | 新实现的处理 |
| --- | --- | --- |
| P2P 引入 `node-datachannel`，使用可靠有序 DataChannel 桥接本机 TCP，包含握手通道和业务通道 | `dist/common/hub/p2p.js:1`、`:80`、`:186`、`:250` | 使用纯 Go Pion，独立通道和 RPC，避免该原生 Node 绑定的交叉编译需求 |
| 原桥接存在信用额度、16 KiB 分块和发送缓冲上限 | `dist/common/hub/p2p.js:3`、`:80` | 使用 12 KiB 分块、固定接收队列、发送缓冲上限；具体帧格式独立 |
| 访客证明使用设备/请求/随机值绑定的 HMAC | `dist/common/hub/guest.js:1` | 重新设计双向角色标签，并绑定本次 TLS exporter、频道和设备 |
| 原逻辑协议名为 `endlesst.device/1`，WebSocket token 与逻辑名不同，使用 JSON-RPC 2.0 schema | `dist/common/device/protocol.js:4`、`:15` | 使用本项目 NDJSON v1，不复用官方协议标识、不承诺兼容 |
| Host 服务调用系统 shell，并动态引入 `node-pty` 提供 PTY | `dist/terminal/services/host-service.js:10`、`:63` | 使用 Go `os/exec`、Unix PTY 和 Windows ConPTY，增加独立 argv 入口 |
| Host 输出有总量上限，过量会终止执行并报告错误 | `dist/terminal/services/host-service.js:36` | 采用有界采集、明确截断、完整已采集结果的分块 artifact |
| 串口环形缓冲将 TX/RX 保存在同一游标空间并按方向筛选 | `dist/terminal/serial/ring-buffer.js:13` | 使用固定大小字节环、方向区间和带随机会话代次的不透明游标 |
| 会话 broker 区分物理串口、服务端会话、客户端流，以及写入者和观察者 | `dist/terminal/services/session-broker.js:6`、`:68` | Agent 持有物理资源；远程连接与 daemon 中的逻辑成员独立 |
| 现场 Agent 区分 ICE 信令、DataChannel 和 relay 路径 | `dist/terminal/agent/manager.js:6`、`:16` | 实现自建 Hub 的信令/中继和可诊断的数据通道，不连接官方服务 |

其中“P2P 控制路径使用 DataChannel”比原设计文档仅凭帮助文本做出的判断有了更强的交付代码证据。本次没有执行原程序的远程业务，因此这些结论仍是静态代码观察，不是原产品端到端运行复现。

## 范围与未知项

REA CLI 分析解析了 1 个 JavaScript 文件，访问 118435 个 AST 节点；Terminal 选定分析解析了 13 个业务模块、访问 24250 个 AST 节点，无解析失败。

Terminal 全包库存包含 23085 个相关文件，选定模块之外的 23072 个文件不在本次聚焦分析范围。REA 报告语义关系覆盖不完整，并达到语义 unknown 记录上限；没有把缺失关系解释为不存在。早期对整个 ASAR 的分析在转为聚焦模块时被中断，其残留结果保留在本机 `terminal-3.1.24.json`，不作为完整分析的依据。

尚未确认原 Hub 的完整实现、官方账号授权链和真实流量计费规则；没有验证原应用的所有硬件、音视频或 Matter 功能。新程序的真实运行、跨平台编译和待完成内容分别记录在 `docs/status.md`，不能用原程序的能力替代新程序的验证。


## Windows 界面静态参考（2026-10-23）

本次继续通过 REA 分析同一 `app.asar` 中的 3 个界面模块：
`dist/terminal/electron/main.js`、`dist/terminal/web/hub-settings.js`、`dist/terminal/web/desktop-controller.js`。
REA 父 Evidence 为 `ev_287c36cb1a40e86611e300e255128f3875d4d8acd8fe881bbab6705fb278aefb`，
摘要 Evidence 为 `ev_7e3dcb2886da5b80ec883be541ef44b9488686b8acc1146b2abc431deb90cd29`。
结果位于本机 `research/evidence/agent-ui-reference.bundle.json` 与 `.summary.json`。
其余 23082 个库存文件不属于这次选定分析范围。

静态观察：原应用使用 Electron 独立窗口、本机回环 HTTP 服务、单实例机制，并提供 Hub 设置和桌面控制；
安装包的页面可见终端标签、SSH、串口与能力权限入口。窗口代码包含托盘保活选项。

独立实现采用纯 Go 的 Windows WebView2 窗口加内嵌 HTML/xterm.js，不依赖 Electron 或原项目 Node 原生模块。
窗口进程隔离运行时故障，Agent 与 CLI 共用同一运行层；当前实现不包含系统托盘。
上述是静态结构参考，不表示原程序已在本机运行验证，也不表示新实现与原程序协议兼容。
