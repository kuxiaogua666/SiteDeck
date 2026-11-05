"""Copy standalone runtime files into the deployment directory.

Existing deployed configuration is preserved. Optional state migration only
copies missing, named Hub files and refuses to replace differing credentials.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import tempfile
import zipfile

ROOT = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser()
parser.add_argument("--output", type=Path, default=ROOT.parent / "sitedeck-dis")
parser.add_argument("--migrate-state", type=Path, help="stopped Hub's existing .local directory")
parser.add_argument("--all-platforms", action="store_true", help="also copy cross-platform releases into platforms/")
args = parser.parse_args()
destination = args.output.resolve()
if destination == ROOT:
    raise SystemExit("The deployment directory must differ from the source directory")

operating_system = {"Linux": "linux", "Darwin": "darwin", "Windows": "windows"}.get(platform.system())
architecture = {"x86_64": "amd64", "AMD64": "amd64", "aarch64": "arm64", "arm64": "arm64", "armv7l": "arm-v7", "riscv64": "riscv64"}.get(platform.machine())
if not operating_system or not architecture:
    raise SystemExit("Unsupported host platform; copy the matching compiled release manually")
target = operating_system + "-" + architecture
suffix = ".exe" if operating_system == "windows" else ""
native_directory = ROOT / "bin"
if not all((native_directory / ("site-" + role + suffix)).is_file() for role in ["hub", "agent", "cli"]):
    native_directory = ROOT / "bin" / target
sources = [(native_directory / ("site-" + role + suffix), destination / "bin" / ("site-" + role + suffix)) for role in ["hub", "agent", "cli"]]
for source, _ in sources:
    if not source.is_file():
        raise SystemExit(f"Missing {source}; build the matching release first")

def install(source, output, mode=0o755):
    output.parent.mkdir(parents=True, exist_ok=True)
    descriptor, temporary = tempfile.mkstemp(prefix=".deploy-", dir=output.parent)
    try:
        with os.fdopen(descriptor, "wb") as stream:
            with source.open("rb") as incoming:
                shutil.copyfileobj(incoming, stream)
            os.chmod(temporary, mode)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, output)
    finally:
        Path(temporary).unlink(missing_ok=True)

for source, output in sources:
    install(source, output)
if operating_system == "linux":
    for source in (ROOT / "deploy/linux").glob("*.sh"):
        install(source, destination / source.name)

if args.migrate_state:
    incoming = args.migrate_state.resolve()
    if incoming == destination / ".local":
        raise SystemExit("Source and destination state directories must differ")
    for name in ["hub.key", "hub-admin.key", "hub-state.json", "hub.log"]:
        source, output = incoming / name, destination / ".local" / name
        if not source.exists():
            continue
        if output.exists():
            if output.read_bytes() != source.read_bytes():
                raise SystemExit(f"Refusing to overwrite existing deployed state: {output}")
            continue
        output.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        with output.open("xb") as stream:
            os.chmod(output, 0o600)
            stream.write(source.read_bytes())

if args.all_platforms:
    for source in (ROOT / "bin").glob("*/*"):
        if source.is_file() and source.name.startswith("site-"):
            install(source, destination / "platforms" / source.parent.name / source.name)

    manifest_source = ROOT / "bin/manifest.json"
    if manifest_source.is_file():
        install(manifest_source, destination / "release-manifest.json", 0o644)
    version = re.search(r'const Version = "([^"]+)"', (ROOT / "internal/protocol/protocol.go").read_text()).group(1)
    for target_directory in sorted((destination / "platforms").glob("windows-*")):
        if not (target_directory / "site-agent-ui.exe").is_file():
            continue
        install(ROOT / "docs/agent-ui.md", target_directory / "README.md", 0o644)
        install(ROOT / "docs/renaming.md", target_directory / "renaming.md", 0o644)
        install(ROOT / "docs/easy-deployment.md", target_directory / "easy-deployment.md", 0o644)
        install(ROOT / "deploy/THIRD_PARTY_UI_NOTICES.txt", target_directory / "THIRD_PARTY_UI_NOTICES.txt", 0o644)
        install(ROOT / "deploy/windows/start-agent.cmd", target_directory / "start-agent.cmd", 0o644)
        archive_directory = destination / "releases"
        archive_directory.mkdir(parents=True, exist_ok=True)
        archive = archive_directory / f"sitedeck-{version}-{target_directory.name}.zip"
        descriptor, temporary = tempfile.mkstemp(prefix=".release-", dir=archive_directory)
        os.close(descriptor)
        try:
            with zipfile.ZipFile(temporary, "w", compression=zipfile.ZIP_DEFLATED) as release:
                release_files = ["site-hub.exe", "site-agent.exe", "site-agent-ui.exe", "site-cli.exe",
                                 "README.md", "easy-deployment.md", "renaming.md", "THIRD_PARTY_UI_NOTICES.txt", "start-agent.cmd"]
                for name in release_files:
                    file = target_directory / name
                    if file.is_file():
                        release.write(file, target_directory.name + "/" + file.name)
            os.replace(temporary, archive)
        finally:
            Path(temporary).unlink(missing_ok=True)
    release_directory = destination / "releases"
    if release_directory.exists():
        sums = [hashlib.sha256(file.read_bytes()).hexdigest() + "  " + file.name
                for file in sorted(release_directory.glob("*.zip"))]
        (release_directory / "SHA256SUMS").write_text("\n".join(sums) + "\n")

install(ROOT / "docs/easy-deployment.md", destination / "easy-deployment.md", 0o644)
install(ROOT / "deploy/site-agent.example.json", destination / "site-agent.example.json", 0o644)
install(ROOT / "docs/agent-ui.md", destination / "Agent-UI.md", 0o644)
install(ROOT / "docs/renaming.md", destination / "Renaming.md", 0o644)
install(ROOT / "deploy/THIRD_PARTY_UI_NOTICES.txt", destination / "THIRD_PARTY_UI_NOTICES.txt", 0o644)

readme = """# SiteDeck 运行目录

当前命名版本为 0.5.1，旧身份和配对兼容说明见 `Renaming.md`。

源码位于相邻的 `sitedeck/`，本目录用于实际部署与运行。

0.5.0 新增 Windows 图标、串口 HEX 收发／时间戳和 UDP、TCP 网络调试助手，见 `Agent-UI.md`。
支持程序旁 `site-agent.json` 自动配置、无 UI 首次设置及文字配对，见 `easy-deployment.md`。
私有预配置 Agent 包在 `preconfigured/`（包含注册凭据）；只需解压运行，不复制设备身份。
如需管理已经部署的公网 Hub，请查看 `Server-Hub.md`；下面的启动脚本只管理本机 Hub。

Windows 普通 x64 设备使用 `platforms/windows-amd64/site-agent-ui.exe`，ARM64 使用对应目录。
双击打开 Agent 窗口；`site-agent.exe` 无参数也打开窗口，带 `run/init/info/rotate` 参数保留 CLI。
窗口使用 WebView2 Runtime，启动失败时提示并尝试默认浏览器。完整 Windows 包在 `releases/`。
首次使用在连接设置中配置 Hub 和能力，保存后启动服务；只使用本机工具可关闭远程接入。
Agent 界面默认端口为 `127.0.0.1:48767`，说明见 `Agent-UI.md`。

Linux 管理命令（从任意目录调用均使用本运行目录的配置）：

```sh
./start-hub.sh
./status-hub.sh
./stop-hub.sh
```

默认管理地址：`http://127.0.0.1:48763/admin/`。
登录时选择本目录的 `.local/hub-admin.key`。
日志位于 `.local/hub.log`；持久配置和授权位于 `.local/hub-state.json`。

程序位于 `bin/`，直接运行 `./bin/site-hub serve` 不依赖 Go、Node.js 或 Python。
其他系统使用相应的二进制。Linux 辅助脚本依赖常见 shell 工具、`setsid` 和 `/proc`。

需要修改监听地址时：`./start-hub.sh --listen 127.0.0.1:48769`。
这不是开机自启服务；本机重启后需再次启动。

更新程序：从源码目录执行 `python3 scripts/deploy.py`，然后在本目录重启 Hub。
部署脚本不覆盖本目录的现有配置。首次从其他运行目录迁移状态时，先停止旧 Hub，
再显式传入 `--migrate-state /旧目录/.local`。
"""
output = destination / "README.md"
output.write_text(readme, encoding="utf-8")
print(json.dumps({"deploymentDirectory": str(destination), "nativeTarget": target, "binaries": [str(output.relative_to(destination)) for _, output in sources], "statePreserved": True}, ensure_ascii=False))
