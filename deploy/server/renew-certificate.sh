#!/bin/sh
set -eu
umask 077
HUB_ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
"$HUB_ROOT/tools/certbot/bin/certbot" renew --non-interactive --no-random-sleep-on-renew \
    --config-dir "$HUB_ROOT/acme/config" --work-dir "$HUB_ROOT/acme/work" \
    --logs-dir "$HUB_ROOT/acme/logs" --deploy-hook "$HUB_ROOT/scripts/publish-certificate.sh"
# Certbot may report success despite a failed deploy hook. Retry publishing and
# propagate its result, including when a previous renewal already issued a cert.
"$HUB_ROOT/scripts/publish-certificate.sh"
