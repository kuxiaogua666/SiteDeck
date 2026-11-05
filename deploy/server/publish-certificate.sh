#!/bin/sh
set -eu
umask 077
HUB_ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
SERVICE_PREFIX=$(cat "$HUB_ROOT/secrets/service-prefix")
case "$SERVICE_PREFIX" in sitedeck|endless) ;; *) echo 'Invalid service prefix' >&2; exit 1;; esac
GATEWAY_SERVICE="$SERVICE_PREFIX-gateway.service"
CERT_SOURCE=${RENEWED_LINEAGE:-$HUB_ROOT/acme/config/live/$SERVICE_PREFIX-hub}
HUB_HOST=$(cat "$HUB_ROOT/secrets/server-host")
case "$HUB_HOST" in ''|*[!A-Za-z0-9.:-]*) echo 'Invalid server-host setting' >&2; exit 1;; esac
if python3 -c 'import ipaddress,sys; ipaddress.ip_address(sys.argv[1])' "$HUB_HOST" >/dev/null 2>&1; then
    openssl x509 -in "$CERT_SOURCE/cert.pem" -noout -checkip "$HUB_HOST"
else
    openssl x509 -in "$CERT_SOURCE/cert.pem" -noout -checkhost "$HUB_HOST"
fi
openssl x509 -in "$CERT_SOURCE/cert.pem" -noout -checkend 86400
CERT_SERIAL=$(openssl x509 -in "$CERT_SOURCE/cert.pem" -noout -serial | cut -d= -f2)
case "$CERT_SERIAL" in ''|*[!0-9A-Fa-f]*) exit 1;; esac
RELEASE="$HUB_ROOT/tls/releases/$CERT_SERIAL"
mkdir -p "$RELEASE"
install -m 600 "$CERT_SOURCE/fullchain.pem" "$RELEASE/fullchain.pem"
install -m 600 "$CERT_SOURCE/privkey.pem" "$RELEASE/privkey.pem"
CERT_PUBLIC=$(openssl x509 -in "$RELEASE/fullchain.pem" -pubkey -noout | openssl sha256)
KEY_PUBLIC=$(openssl pkey -in "$RELEASE/privkey.pem" -pubout | openssl sha256)
[ "$CERT_PUBLIC" = "$KEY_PUBLIC" ]
ln -s "releases/$CERT_SERIAL" "$HUB_ROOT/tls/.current-$$"
mv -Tf "$HUB_ROOT/tls/.current-$$" "$HUB_ROOT/tls/current"
if systemctl is-active --quiet "$GATEWAY_SERVICE"; then
    LOADED=$(cat "$HUB_ROOT/tls/loaded-serial" 2>/dev/null || true)
    if [ "$LOADED" != "$CERT_SERIAL" ] || [ "${SITEDECK_FORCE_RELOAD:-0}" = 1 ]; then
        # Validation and reload run inside the gateway's own systemd sandbox.
        systemctl reload "$GATEWAY_SERVICE"
    fi
    python3 "$HUB_ROOT/scripts/verify-certificate.py" "$CERT_SERIAL"
    printf '%s\n' "$CERT_SERIAL" > "$HUB_ROOT/tls/.loaded-$$"
    mv -f "$HUB_ROOT/tls/.loaded-$$" "$HUB_ROOT/tls/loaded-serial"
fi
