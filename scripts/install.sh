#!/usr/bin/env bash
# DyroKol installer for Linux (and Termux).
#
#   curl -fsSL https://raw.githubusercontent.com/dnech/DyroKol/main/scripts/install.sh | sudo bash
#   curl -fsSL https://raw.githubusercontent.com/dnech/DyroKol/main/scripts/install.sh | sudo bash -s -- --role server
#
# Downloads the release binary for this machine from GitHub Releases, verifies
# its SHA-256 against the release's SHA256SUMS, installs it and (with --role)
# sets up a systemd service.
set -euo pipefail

REPO="dnech/DyroKol"
VERSION="latest"
ROLE=""
PREFIX_DIR=""
DATA_DIR="/var/lib/dyrokol"
SVC_USER="dyrokol"
UNINSTALL=0
PURGE=0

usage() {
  cat <<USAGE
Usage: install.sh [options]

  --role server|client   also install and enable a systemd unit for this role
  --version TAG          release tag to install, e.g. v1.0.0 (default: latest)
  --prefix DIR           install the binary into DIR (default: /usr/local/bin)
  --data-dir DIR         data directory of the service (default: $DATA_DIR)
  --uninstall            remove the binary and the systemd units (data is kept)
  --purge                with --uninstall: also delete the data directory
  -h, --help             show this help

Environment: DYROKOL_BASE_URL overrides the download location (a directory that
holds dyrokol-<os>-<arch> and SHA256SUMS), e.g. for a mirror or a local test.
USAGE
}

die() { echo "install.sh: $*" >&2; exit 1; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --role)     ROLE="${2:-}"; shift 2 ;;
    --version)  VERSION="${2:-}"; shift 2 ;;
    --prefix)   PREFIX_DIR="${2:-}"; shift 2 ;;
    --data-dir) DATA_DIR="${2:-}"; shift 2 ;;
    --uninstall) UNINSTALL=1; shift ;;
    --purge)    PURGE=1; shift ;;
    -h|--help)  usage; exit 0 ;;
    *) usage >&2; die "unknown option: $1" ;;
  esac
done
case "$ROLE" in ""|server|client) ;; *) die "--role must be server or client" ;; esac

[[ "$(uname -s)" == Linux ]] || die "this installer supports Linux and Termux only (Windows: see README)"

# Termux has no systemd and no root: install into $PREFIX/bin.
TERMUX=0
if [[ -n "${TERMUX_VERSION:-}" || "${PREFIX:-}" == */com.termux/* ]]; then TERMUX=1; fi

if [[ $TERMUX -eq 1 ]]; then
  BIN_DIR="${PREFIX_DIR:-${PREFIX:-/data/data/com.termux/files/usr}/bin}"
  ROLE=""
else
  BIN_DIR="${PREFIX_DIR:-/usr/local/bin}"
  # root is needed for /usr/local/bin and for services; a custom --prefix without --role works as a user
  [[ $EUID -eq 0 || ( -n "$PREFIX_DIR" && -z "$ROLE" ) ]] || die "run as root (use sudo)"
fi
BIN="$BIN_DIR/dyrokol"

if [[ $UNINSTALL -eq 1 ]]; then
  if command -v systemctl >/dev/null 2>&1; then
    for u in dyrokol-server dyrokol-client; do
      systemctl disable --now "$u" 2>/dev/null || true
      rm -f "/etc/systemd/system/$u.service"
    done
    systemctl daemon-reload || true
  fi
  rm -f "$BIN"
  if [[ $PURGE -eq 1 ]]; then rm -rf "$DATA_DIR"; fi
  echo "DyroKol removed. Data directory $DATA_DIR $([[ $PURGE -eq 1 ]] && echo deleted || echo kept)."
  exit 0
fi

case "$(uname -m)" in
  x86_64|amd64)  ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  armv7l|armv7|armhf) ARCH=arm ;;
  *) die "unsupported architecture: $(uname -m)" ;;
esac
ASSET="dyrokol-linux-$ARCH"

if [[ -n "${DYROKOL_BASE_URL:-}" ]]; then
  BASE="${DYROKOL_BASE_URL%/}"
elif [[ "$VERSION" == latest ]]; then
  BASE="https://github.com/$REPO/releases/latest/download"
else
  BASE="https://github.com/$REPO/releases/download/$VERSION"
fi

if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL --retry 3 -o "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -q -O "$2" "$1"; }
else
  die "curl or wget is required"
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "Downloading $ASSET ($VERSION)..."
fetch "$BASE/$ASSET" "$TMP/$ASSET" || die "download failed: $BASE/$ASSET"
fetch "$BASE/SHA256SUMS" "$TMP/SHA256SUMS" || die "download failed: $BASE/SHA256SUMS"

want="$(awk -v a="$ASSET" '{f=$2; sub(/^\*/,"",f); n=split(f,p,"/"); if (p[n]==a) {print $1; exit}}' "$TMP/SHA256SUMS")"
[[ -n "$want" ]] || die "$ASSET not listed in SHA256SUMS"
have="$(sha256sum "$TMP/$ASSET" | awk '{print $1}')"
[[ "$want" == "$have" ]] || die "checksum mismatch for $ASSET (expected $want, got $have)"
echo "Checksum OK."

mkdir -p "$BIN_DIR"
install -m 0755 "$TMP/$ASSET" "$BIN"
echo "Installed $BIN"
"$BIN" help >/dev/null 2>&1 || die "the installed binary does not run on this machine"

if [[ $TERMUX -eq 1 ]]; then
  cat <<MSG

Done. Register this device:
  dyrokol client --server your-vps.example.com:7443
To start on boot install Termux:Boot (see docs/en/deployment.md).
MSG
  exit 0
fi

if [[ -z "$ROLE" ]]; then
  cat <<MSG

Done. Run it manually:
  dyrokol server                       # relay on a public host
  dyrokol client --server host:7443    # agent
or install as a service:  re-run with --role server|client
MSG
  exit 0
fi

command -v systemctl >/dev/null 2>&1 || die "systemd not found; start '$BIN $ROLE --dir $DATA_DIR' with your init system"

id "$SVC_USER" >/dev/null 2>&1 || \
  useradd --system --home "$DATA_DIR" --create-home --shell /usr/sbin/nologin "$SVC_USER"
install -d -m 0700 -o "$SVC_USER" -g "$SVC_USER" "$DATA_DIR"

restart=on-failure
[[ $ROLE == client ]] && restart=always
UNIT="/etc/systemd/system/dyrokol-$ROLE.service"
cat >"$UNIT" <<UNITFILE
[Unit]
Description=DyroKol $ROLE
After=network-online.target
Wants=network-online.target

[Service]
User=$SVC_USER
ExecStart=$BIN $ROLE --dir $DATA_DIR
# only needed for ports below 1024 (443, 80, ...)
AmbientCapabilities=CAP_NET_BIND_SERVICE
Restart=$restart
RestartSec=3
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=$DATA_DIR
PrivateTmp=true

[Install]
WantedBy=multi-user.target
UNITFILE
systemctl daemon-reload

if [[ $ROLE == server ]]; then
  systemctl enable --now dyrokol-server
  cat <<MSG

Relay is running (journalctl -u dyrokol-server -f). Open TCP port 7443 in the firewall,
then register the first machine:  dyrokol client --server <this-host>:7443
MSG
else
  # a machine of an already activated relay only needs its token: leave a config
  # with placeholders to fill in
  "$BIN" client --dir "$DATA_DIR" --init >/dev/null
  chown -R "$SVC_USER:$SVC_USER" "$DATA_DIR"
  chmod 600 "$DATA_DIR/dyrokol.yaml"
  systemctl enable dyrokol-client
  if grep -q -E 'PASTE_TOKEN_HERE|YOUR-RELAY-HOST' "$DATA_DIR/dyrokol.yaml"; then
    cat <<MSG

The agent service is enabled but not started: its config still has placeholders.
  1. Machine of an already running relay: edit $DATA_DIR/dyrokol.yaml (relay address,
     the machine's token from the admin UI: Machines -> Add machine), then
       sudo systemctl start dyrokol-client
  2. The very first machine of a new relay: register it instead
       sudo -u $SVC_USER $BIN client --dir $DATA_DIR --server your-vps.example.com
       # press Ctrl+C after "connected", then: sudo systemctl start dyrokol-client
MSG
  else
    systemctl restart dyrokol-client
    echo "Agent service started (journalctl -u dyrokol-client -f)."
  fi
fi
