#!/bin/sh
# Install nordicgopher on a systemd host. Run as root from an unpacked dist/.
#
# Idempotent: safe to re-run for an upgrade. It deliberately does NOT start
# the service, because the service cannot work until /etc/nordicgopher/env
# names the public hostname, and a wrong hostname there silently breaks every
# menu link for remote clients.
set -eu

PREFIX=/usr/local/bin
STATE=/var/lib/nordicgopher
CONF=/etc/nordicgopher
UNITS=/etc/systemd/system
USER=nordicgopher

[ "$(id -u)" = 0 ] || { echo "run as root" >&2; exit 1; }
command -v systemctl >/dev/null || { echo "systemd not found" >&2; exit 1; }

echo "==> service account"
if ! id "$USER" >/dev/null 2>&1; then
    useradd --system --home-dir "$STATE" --shell /usr/sbin/nologin "$USER"
    echo "    created $USER"
else
    echo "    $USER exists"
fi

echo "==> directories"
install -d -o "$USER" -g "$USER" -m 755 "$STATE" "$STATE/cache"
install -d -o root -g "$USER" -m 750 "$CONF"

echo "==> binaries"
for b in nordicgopher ngingest ngconv; do
    install -o root -g root -m 755 "$b" "$PREFIX/$b"
    echo "    $PREFIX/$b"
done

echo "==> download catalogue"
if [ -f "$STATE/artifacts.json" ]; then
    echo "    keeping existing $STATE/artifacts.json"
else
    install -o root -g "$USER" -m 644 artifacts.json "$STATE/artifacts.json"
fi

echo "==> unit files"
install -o root -g root -m 644 nordicgopher.service "$UNITS/"
install -o root -g root -m 644 nordicgopher-ingest.service "$UNITS/"
install -o root -g root -m 644 nordicgopher-ingest.timer "$UNITS/"
systemctl daemon-reload

echo "==> configuration"
if [ -f "$CONF/env" ]; then
    echo "    keeping existing $CONF/env"
else
    install -o root -g "$USER" -m 640 env.example "$CONF/env"
    echo "    wrote $CONF/env from the template -- EDIT IT before starting"
fi

cat <<'NEXT'

Installed. Remaining steps, in order:

  1. Edit /etc/nordicgopher/env.
     PUBLIC_HOST is embedded in every menu line: if it is wrong, the mirror
     works locally and every link is broken for everyone else.

  2. Build the content tree once, in the foreground, and watch it:
       systemctl start nordicgopher-ingest.service
       journalctl -u nordicgopher-ingest.service -f
     It takes a couple of minutes cold. Expect "content tree written".

  3. Start serving, and enable the nightly rebuild:
       systemctl enable --now nordicgopher.service
       systemctl enable --now nordicgopher-ingest.timer

  4. Check it from the host itself before opening the firewall:
       printf '/\r\n' | nc localhost 7070 | head
     Confirm the host and port in the menu lines are the public ones.

  5. Open the port: security group inbound TCP 7070 from 0.0.0.0/0,
     and the host firewall if one is running.

NEXT
