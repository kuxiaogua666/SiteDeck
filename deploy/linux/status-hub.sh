#!/bin/sh
set -eu
HUB_ROOT=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
cd "$HUB_ROOT"
. ./hub-process.sh
if read_hub_pid && hub_is_owned; then
    echo "Hub 运行中，PID=$HUB_PID"
    echo "运行目录：$HUB_ROOT"
else
    echo 'Hub 未运行，或运行记录不匹配。'
    exit 1
fi
