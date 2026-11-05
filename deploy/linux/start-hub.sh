#!/bin/sh
set -eu
umask 077
HUB_ROOT=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
cd "$HUB_ROOT"
. ./hub-process.sh
mkdir -p .local
[ -d /proc ] || { echo '此辅助脚本需要 Linux /proc；其他系统请直接运行 bin/site-hub serve。' >&2; exit 1; }
command -v setsid >/dev/null 2>&1 || { echo '后台启动需要 setsid；也可以直接在终端运行 bin/site-hub serve。' >&2; exit 1; }
mkdir .local/hub-start.lock 2>/dev/null || { echo '另一次启动尚未结束；请检查 .local/hub-start.lock。' >&2; exit 1; }
trap 'rmdir .local/hub-start.lock 2>/dev/null || true' EXIT

if read_hub_pid; then
    if hub_is_owned; then
        echo "Hub 已在运行，PID=$HUB_PID"
        exit 0
    fi
    if hub_is_alive; then
        echo 'PID 文件指向无法确认归属的进程，拒绝重复启动。' >&2
        exit 1
    fi
fi

./bin/site-hub init > .local/hub-init.json
nohup setsid ./bin/site-hub serve "$@" >> .local/hub.log 2>&1 < /dev/null &
HUB_PID=$!
HUB_STARTED=$(awk '{print $22}' "/proc/$HUB_PID/stat")
printf '%s\n' "$HUB_STARTED" > .local/hub.started
printf '%s\n' "$HUB_PID" > .local/hub.pid

HUB_ATTEMPT=0
while [ "$HUB_ATTEMPT" -lt 25 ]; do
    sleep 0.2
    if hub_is_owned; then
        echo "Hub 已启动，PID=$HUB_PID"
        echo "运行目录：$HUB_ROOT"
        echo "日志：$HUB_ROOT/.local/hub.log"
        exit 0
    fi
    if ! hub_is_alive; then
        echo 'Hub 启动失败：' >&2
        tail -n 15 .local/hub.log >&2
        exit 1
    fi
    HUB_ATTEMPT=$((HUB_ATTEMPT + 1))
done
echo '无法验证 Hub 启动状态，请检查 .local/hub.log。' >&2
exit 1
