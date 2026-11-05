#!/bin/sh
set -eu
HUB_ROOT=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
cd "$HUB_ROOT"
. ./hub-process.sh
if ! read_hub_pid; then
    echo 'Hub 没有运行记录。'
    exit 0
fi
if ! hub_is_alive; then
    echo 'Hub 已停止。'
    exit 0
fi
if ! hub_is_owned; then
    echo '进程归属不匹配，拒绝终止。' >&2
    exit 1
fi
kill -TERM "$HUB_PID"
HUB_ATTEMPT=0
while hub_is_owned && [ "$HUB_ATTEMPT" -lt 60 ]; do
    sleep 0.2
    HUB_ATTEMPT=$((HUB_ATTEMPT + 1))
done
if hub_is_owned; then
    echo 'Hub 尚未退出，请检查日志。' >&2
    exit 1
fi
rm -f .local/hub.pid .local/hub.started
echo 'Hub 已停止。'
