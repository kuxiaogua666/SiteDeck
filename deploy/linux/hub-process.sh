#!/bin/sh
# Shared ownership checks for the Linux deployment helpers.
read_hub_pid() {
    HUB_PID=""
    HUB_STARTED=""
    [ -r .local/hub.pid ] && [ -r .local/hub.started ] || return 1
    HUB_PID=$(cat .local/hub.pid)
    HUB_STARTED=$(cat .local/hub.started)
    case "$HUB_PID:$HUB_STARTED" in *[!0-9:]*|:*) return 1 ;; esac
    [ -n "$HUB_PID" ] && [ -n "$HUB_STARTED" ]
}

hub_is_alive() {
    [ -r "/proc/$HUB_PID/stat" ] || return 1
    [ "$(awk '{print $3}' "/proc/$HUB_PID/stat" 2>/dev/null)" != Z ]
}

hub_is_owned() {
    hub_is_alive || return 1
    [ "$(awk '{print $22}' "/proc/$HUB_PID/stat" 2>/dev/null)" = "$HUB_STARTED" ] || return 1
    [ "$(readlink "/proc/$HUB_PID/cwd" 2>/dev/null)" = "$HUB_ROOT" ] || return 1
    HUB_EXE=$(readlink "/proc/$HUB_PID/exe" 2>/dev/null) || return 1
    case "$HUB_EXE" in
        "$HUB_ROOT/bin/site-hub"|"$HUB_ROOT/bin/site-hub (deleted)") return 0 ;;
        *) return 1 ;;
    esac
}
