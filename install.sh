#!/usr/bin/env bash
# install.sh — easy server install AND update for WellKeptLib.
#
# One script, two jobs:
#   * No existing install found  -> fresh install (user-level systemd
#     service, optional reverse-proxy snippet).
#   * Existing install found      -> update in place: timestamped
#     backups, swap the binary, verify the new version answers, and
#     roll back automatically if it does not.
#
# It never uses sudo (it may PRINT a sudo command for you to run),
# never touches the data directory except to back up config.json,
# and never edits a stranger's reverse-proxy config — on hosts that
# are not Ultra-shaped it prints the snippet instead.
#
# Usage:
#   ./install.sh [--port N] [--uninstall] [--yes] [--help]
# Environment:
#   PFM_PORT   port to use on a fresh install (same as --port)
#   PFM_BIND   proxy (default; listen on 127.0.0.1, reverse proxy in
#              front) or lan (listen on 0.0.0.0 — read the warning in
#              the script output before choosing this)
#
# Run it from the extracted release folder: it installs the binary
# and VERSION file that sit beside it.

set -euo pipefail

BASE_PATH="/plex-file-manager"
SERVICE="plex-file-manager.service"
BIN_DIR="$HOME/bin"
BIN_PATH="$HOME/bin/plex-file-manager"
DATA_DIR="$HOME/.local/share/plex-file-manager"
UNIT_DIR="$HOME/.config/systemd/user"
UNIT_PATH="$UNIT_DIR/$SERVICE"
NGINX_SNIPPET="$HOME/.apps/nginx/proxy.d/plex-file-manager.conf"
SNIPPET_MARKER="# plex-file-manager (installed by install.sh)"
FFMPEG_TARBALL_BASE="https://johnvansickle.com/ffmpeg/releases"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

OPT_PORT="${PFM_PORT:-}"
OPT_BIND="${PFM_BIND:-}"
ASSUME_YES=0
DO_UNINSTALL=0

say()  { printf '%s\n' "$*"; }
warn() { printf 'WARNING: %s\n' "$*" >&2; }
die()  { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

usage() {
    cat <<EOF
Usage: ./install.sh [--port N] [--uninstall] [--yes] [--help]

  --port N     Port for a fresh install (or set PFM_PORT).
  --uninstall  Remove the service and binary. The data directory is
               kept unless you explicitly say to delete it.
  --yes        Accept the default at every prompt (automation).
  --help       This text.

Environment: PFM_PORT, PFM_BIND=proxy|lan (see the script header).
EOF
}

# ask "question" "default" -> prints the answer on stdout.
ask() {
    local q="$1" def="${2:-}" ans=""
    if [ "$ASSUME_YES" = "1" ]; then printf '%s' "$def"; return 0; fi
    printf '%s' "$q" >&2
    read -r ans || ans=""
    if [ -z "$ans" ]; then ans="$def"; fi
    printf '%s' "$ans"
}

# confirm_yesno "question" -> returns 0 for yes, 1 for no (default no).
confirm_yesno() {
    local q="$1" ans
    if [ "$ASSUME_YES" = "1" ]; then return 1; fi
    printf '%s [y/N] ' "$q" >&2
    read -r ans || ans=""
    case "$ans" in y|Y|yes|YES) return 0 ;; *) return 1 ;; esac
}

port_free() {
    local p="$1"
    if have ss; then
        if ss -tln 2>/dev/null | grep -Eq "[:.]${p}[[:space:]]"; then
            return 1
        fi
        return 0
    fi
    # Fallback: a successful TCP connect means something is listening.
    if (exec 3<>"/dev/tcp/127.0.0.1/$p") 2>/dev/null; then
        return 1
    fi
    return 0
}

# state_version PORT -> prints the app's version, or nothing.
state_version() {
    local p="$1" out
    if ! have curl; then return 0; fi
    out="$(curl -s --max-time 3 "http://127.0.0.1:${p}${BASE_PATH}/api/state" 2>/dev/null || true)"
    printf '%s' "$out" | sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1
}

# wait_for_version PORT WANT SECONDS -> 0 once /api/state says WANT.
wait_for_version() {
    local p="$1" want="$2" secs="$3" i v
    for i in $(seq 1 "$secs"); do
        v="$(state_version "$p")"
        if [ "$v" = "$want" ]; then return 0; fi
        sleep 1
    done
    return 1
}

# wait_for_answer PORT SECONDS -> 0 once /api/state answers at all.
wait_for_answer() {
    local p="$1" secs="$2" i v
    for i in $(seq 1 "$secs"); do
        v="$(state_version "$p")"
        if [ -n "$v" ]; then printf '%s' "$v"; return 0; fi
        sleep 1
    done
    return 1
}

systemd_ok() { have systemctl; }
svc_stop()  { if systemd_ok; then systemctl --user stop "$SERVICE" 2>/dev/null || true; fi; }
svc_start() { if systemd_ok; then systemctl --user start "$SERVICE" 2>/dev/null; fi; }

# ---------------------------------------------------------------- ffmpeg
FFMPEG_STATUS=""
check_ffmpeg() {
    if have ffprobe && have ffmpeg; then
        FFMPEG_STATUS="found: ffmpeg=$(command -v ffmpeg), ffprobe=$(command -v ffprobe)"
        say "ffmpeg/ffprobe: $FFMPEG_STATUS"
        return 0
    fi
    say "ffmpeg/ffprobe: NOT both found on PATH (the app needs them to probe media files)."
    FFMPEG_STATUS="missing"
    local arch tarball
    case "$(uname -m)" in
        x86_64|amd64)   tarball="ffmpeg-release-amd64-static.tar.xz" ;;
        aarch64|arm64)  tarball="ffmpeg-release-arm64-static.tar.xz" ;;
        *)              tarball="" ;;
    esac
    if [ -n "$tarball" ] && [ "$ASSUME_YES" != "1" ]; then
        if confirm_yesno "Download a static ffmpeg build into ~/bin now?"; then
            download_ffmpeg "$tarball" || true
        fi
    elif [ -n "$tarball" ] && [ "$ASSUME_YES" = "1" ]; then
        download_ffmpeg "$tarball" || true
    fi
    if have ffprobe && have ffmpeg; then
        FFMPEG_STATUS="installed static build into ~/bin: ffmpeg=$HOME/bin/ffmpeg, ffprobe=$HOME/bin/ffprobe"
        say "ffmpeg/ffprobe: $FFMPEG_STATUS"
    else
        FFMPEG_STATUS="missing — after installing, paste the FULL paths of ffprobe and ffmpeg into the app's Settings tab (Settings does not expand ~, so use the full path, e.g. $HOME/bin/ffprobe)"
        say "Continuing without ffmpeg for now. $FFMPEG_STATUS"
    fi
}

download_ffmpeg() {
    local tarball="$1" tmp
    tmp="$HOME/ffmpeg-static"
    mkdir -p "$HOME/bin" "$tmp"
    say "Downloading $tarball ..."
    if ! ( cd "$tmp" \
        && curl -fsSL -o "$tarball" "$FFMPEG_TARBALL_BASE/$tarball" \
        && tar xf "$tarball" ); then
        warn "ffmpeg download/extract failed (no network?). Continuing the install without it."
        return 1
    fi
    local d
    d="$(find "$tmp" -maxdepth 1 -type d -name 'ffmpeg-*-static' | head -1)"
    if [ -z "$d" ] || [ ! -f "$d/ffmpeg" ]; then
        warn "static ffmpeg build not found after extraction. Continuing without it."
        return 1
    fi
    cp "$d/ffmpeg" "$d/ffprobe" "$HOME/bin/"
    chmod +x "$HOME/bin/ffmpeg" "$HOME/bin/ffprobe"
    return 0
}

# ---------------------------------------------------------------- proxy
ultra_shaped() { [ -d "$HOME/.apps/nginx/proxy.d" ]; }
PUBLIC_URL=""
write_proxy_snippet() {
    local port="$1"
    mkdir -p "$(dirname "$NGINX_SNIPPET")"
    cat > "$NGINX_SNIPPET" <<EOF
$SNIPPET_MARKER
location /plex-file-manager/ {
    proxy_pass              http://127.0.0.1:${port};
    proxy_http_version      1.1;
    proxy_set_header        X-Forwarded-Host        \$http_host;
    proxy_set_header        X-Forwarded-Proto       \$scheme;
}
EOF
    if have app-nginx; then app-nginx restart >/dev/null 2>&1 || true; fi
    local host
    host="$(hostname)"
    case "$host" in
        *.*) PUBLIC_URL="https://$(whoami).${host}${BASE_PATH}" ;;
        *)   PUBLIC_URL="" ;;
    esac
}

print_proxy_snippet() {
    local port="$1"
    cat <<EOF

--- reverse proxy snippet (add this to your nginx server block, then reload nginx) ---
location /plex-file-manager/ {
    proxy_pass              http://127.0.0.1:${port};
    proxy_http_version      1.1;
    proxy_set_header        X-Forwarded-Host        \$http_host;
    proxy_set_header        X-Forwarded-Proto       \$scheme;
}
--- end snippet ---
EOF
}

# ---------------------------------------------------------------- uninstall
do_uninstall() {
    say "Uninstalling WellKeptLib..."
    if [ ! -f "$UNIT_PATH" ] && [ ! -e "$BIN_PATH" ]; then
        say "Nothing to uninstall: no service unit and no binary found."
        return 0
    fi
    if systemd_ok; then
        systemctl --user stop "$SERVICE" 2>/dev/null || true
        systemctl --user disable "$SERVICE" 2>/dev/null || true
    fi
    if [ -f "$UNIT_PATH" ]; then rm -f "$UNIT_PATH"; say "Removed service unit $UNIT_PATH"; fi
    if systemd_ok; then systemctl --user daemon-reload 2>/dev/null || true; fi
    if [ -e "$BIN_PATH" ]; then rm -f "$BIN_PATH"; say "Removed binary $BIN_PATH"; fi
    ls "$BIN_DIR"/plex-file-manager.bak-* >/dev/null 2>&1 && \
        say "Backup binaries in $BIN_DIR (plex-file-manager.bak-*) were kept."
    if [ -f "$NGINX_SNIPPET" ] && grep -q "$SNIPPET_MARKER" "$NGINX_SNIPPET"; then
        rm -f "$NGINX_SNIPPET"
        say "Removed proxy snippet $NGINX_SNIPPET"
        if have app-nginx; then app-nginx restart >/dev/null 2>&1 || true; fi
    fi
    if [ -d "$DATA_DIR" ]; then
        # The data directory holds settings, the scan cache and the
        # Plex sign-in. It is KEPT by default — and --yes NEVER
        # deletes it; only an explicit typed yes at this prompt does.
        if [ "$ASSUME_YES" != "1" ] && confirm_yesno "Also DELETE the data directory $DATA_DIR (settings, cache, sign-ins)? Default is to keep it."; then
            rm -rf "$DATA_DIR"
            say "Deleted data directory $DATA_DIR"
        else
            say "Kept data directory $DATA_DIR"
        fi
    fi
    say ""
    say "=== Summary ==="
    say "Mode: uninstalled"
    say "Service: removed"
    say "Binary: removed"
    say "Data directory: $([ -d "$DATA_DIR" ] && echo "kept at $DATA_DIR" || echo "deleted")"
}

# ---------------------------------------------------------------- main
while [ $# -gt 0 ]; do
    case "$1" in
        --port)      OPT_PORT="${2:?--port needs a value}"; shift 2 ;;
        --port=*)    OPT_PORT="${1#--port=}"; shift ;;
        --uninstall) DO_UNINSTALL=1; shift ;;
        --yes)       ASSUME_YES=1; shift ;;
        --help|-h)   usage; exit 0 ;;
        *)           die "unknown argument: $1 (see --help)" ;;
    esac
done

if [ "$(uname -s)" != "Linux" ]; then
    say "This installer is for Linux servers. On Windows, just run plex-file-manager.exe from the release package — no install needed."
    exit 1
fi

if [ "$DO_UNINSTALL" = "1" ]; then
    do_uninstall
    exit 0
fi

[ -f "$SCRIPT_DIR/VERSION" ] || die "VERSION file not found beside install.sh — run this script from the extracted release folder."
PKG_VERSION="$(tr -d ' \r\n' < "$SCRIPT_DIR/VERSION")"
[ -n "$PKG_VERSION" ] || die "VERSION file beside install.sh is empty."

case "$(uname -m)" in
    x86_64|amd64)   BIN_SRC="$SCRIPT_DIR/plex-file-manager" ;;
    aarch64|arm64)  BIN_SRC="$SCRIPT_DIR/plex-file-manager-arm64" ;;
    *)              die "unsupported CPU architecture: $(uname -m) (this release ships x86_64 and arm64 binaries)" ;;
esac
[ -f "$BIN_SRC" ] || die "binary not found beside install.sh: $BIN_SRC — run this script from the extracted release folder."

say "WellKeptLib v$PKG_VERSION — installer"
say ""

check_ffmpeg
say ""

EXISTING=0
if [ -x "$BIN_PATH" ] && { [ -d "$DATA_DIR" ] || [ -f "$UNIT_PATH" ]; }; then
    EXISTING=1
fi

# ================================================================ UPDATE
if [ "$EXISTING" = "1" ]; then
    say "Existing install detected — running as an UPDATE."
    PORT=""
    if [ -f "$UNIT_PATH" ]; then
        PORT="$(grep -oE '(127\.0\.0\.1|0\.0\.0\.0):[0-9]+' "$UNIT_PATH" | head -1 | cut -d: -f2 || true)"
    fi
    OLD_VERSION=""
    if [ -n "$PORT" ]; then
        OLD_VERSION="$(state_version "$PORT")"
    fi
    say "Current version: ${OLD_VERSION:-unknown}${PORT:+ (port $PORT)}"

    TS="$(date +%Y%m%d-%H%M%S)"
    BIN_BAK="$BIN_PATH.bak-$TS"
    cp "$BIN_PATH" "$BIN_BAK"
    say "Backed up binary -> $BIN_BAK"
    CFG_BAK=""
    if [ -f "$DATA_DIR/config.json" ]; then
        CFG_BAK="$DATA_DIR/config.json.bak-$TS"
        cp "$DATA_DIR/config.json" "$CFG_BAK"
        say "Backed up config -> $CFG_BAK"
    fi

    cp "$BIN_SRC" "$BIN_PATH.new"
    chmod +x "$BIN_PATH.new"
    if have sha256sum; then
        say "New binary sha256: $(sha256sum "$BIN_PATH.new" | awk '{print $1}')"
    fi

    if ! systemd_ok; then
        # No service manager to stop/start: swap the file and let the
        # user restart the app their usual way. (Nothing is running
        # under our control, so there is nothing to verify against.)
        mv "$BIN_PATH.new" "$BIN_PATH"
        say ""
        say "=== Summary ==="
        say "Mode: updated (manual restart needed — no systemd found)"
        say "Version: ${OLD_VERSION:-unknown} -> $PKG_VERSION"
        say "Binary: $BIN_PATH (restart the app to finish the update)"
        say "Backups: $BIN_BAK${CFG_BAK:+, $CFG_BAK}"
        say "ffmpeg: $FFMPEG_STATUS"
        exit 0
    fi

    svc_stop
    mv "$BIN_PATH.new" "$BIN_PATH"

    UPDATED_OK=0
    if svc_start && [ -n "$PORT" ] && wait_for_version "$PORT" "$PKG_VERSION" 30; then
        UPDATED_OK=1
    elif [ -z "$PORT" ]; then
        # No port could be read from the unit; give the service a
        # moment and treat "it started" as the best available check.
        sleep 2
        if systemctl --user is-active --quiet "$SERVICE" 2>/dev/null; then UPDATED_OK=1; fi
    fi

    if [ "$UPDATED_OK" != "1" ]; then
        warn "the updated app did not come up as v$PKG_VERSION — rolling back to the previous binary."
        svc_stop
        cp "$BIN_BAK" "$BIN_PATH"
        svc_start || true
        if [ -n "$PORT" ]; then wait_for_answer "$PORT" 15 >/dev/null || true; fi
        warn "rolled back. The previous version is running again (binary restored from $BIN_BAK)."
        warn "For the failure details see: $DATA_DIR/server.log  (tail -50 $DATA_DIR/server.log)"
        exit 1
    fi

    say ""
    say "=== Summary ==="
    say "Mode: updated"
    say "Version: ${OLD_VERSION:-unknown} -> $PKG_VERSION"
    say "Service: $SERVICE (systemd --user)"
    say "Local check: http://127.0.0.1:${PORT}${BASE_PATH}"
    say "Backups: $BIN_BAK${CFG_BAK:+, $CFG_BAK}"
    say "ffmpeg: $FFMPEG_STATUS"
    say "Next step: open the app, check your library folders under Library roots, and Start scan."
    exit 0
fi

# ================================================================ FRESH INSTALL
say "No existing install found — running as a FRESH INSTALL."
say "On Ultra.cc, use a port from your assigned range (see your service details). Other servers: any free port."

PORT="$OPT_PORT"
if [ -z "$PORT" ]; then
    DEFPORT=15240
    if ! port_free "$DEFPORT"; then
        p=15241
        while [ $p -le 15260 ] && ! port_free "$p"; do p=$((p+1)); done
        DEFPORT=$p
    fi
    PORT="$(ask "Port to listen on [$DEFPORT]: " "$DEFPORT")"
fi
case "$PORT" in ''|*[!0-9]*) die "port must be a number, got: $PORT" ;; esac
if [ "$PORT" -lt 1 ] || [ "$PORT" -gt 65535 ]; then die "port out of range: $PORT"; fi
if ! port_free "$PORT"; then
    die "port $PORT is already in use — pick another (--port N) and re-run."
fi

BIND="$OPT_BIND"
if [ -z "$BIND" ]; then
    if [ "$ASSUME_YES" = "1" ]; then
        BIND="proxy"
    else
        say ""
        say "How will you open the app?"
        say "  1) proxy — behind a reverse proxy (Ultra.cc / nginx / Caddy). Listens on 127.0.0.1 only. [default]"
        say "  2) lan   — directly on this machine's network address (0.0.0.0), no proxy."
        choice="$(ask "Choose 1 or 2 [1]: " "1")"
        case "$choice" in 2|lan) BIND="lan" ;; *) BIND="proxy" ;; esac
    fi
fi
case "$BIND" in
    proxy) ADDR_HOST="127.0.0.1" ;;
    lan)
        ADDR_HOST="0.0.0.0"
        warn "LAN mode: this app has NO login of its own."
        warn "Anyone on your network who can reach port $PORT can use it — including Organize and Delete."
        warn "Only choose this on a trusted home network. For anything else, use proxy mode with basic auth in front."
        ;;
    *) die "PFM_BIND / bind mode must be 'proxy' or 'lan', got: $BIND" ;;
esac

mkdir -p "$BIN_DIR" "$DATA_DIR" "$UNIT_DIR"
cp "$BIN_SRC" "$BIN_PATH"
chmod +x "$BIN_PATH"
say "Installed binary -> $BIN_PATH"

cat > "$UNIT_PATH" <<EOF
[Unit]
Description=WellKeptLib
After=network-online.target

[Service]
Type=exec
Restart=on-failure
ExecStart=%h/bin/plex-file-manager -addr ${ADDR_HOST}:${PORT} -data %h/.local/share/plex-file-manager -baseurl ${BASE_PATH} -no-browser
StandardOutput=file:%h/.local/share/plex-file-manager/server.log
StandardError=file:%h/.local/share/plex-file-manager/server.log

[Install]
WantedBy=default.target
EOF
say "Wrote service unit -> $UNIT_PATH"

MANUAL_START=0
if systemd_ok && systemctl --user daemon-reload 2>/dev/null; then
    systemctl --user enable --now "$SERVICE" 2>/dev/null || MANUAL_START=1
else
    MANUAL_START=1
fi

if [ "$MANUAL_START" = "1" ]; then
    say ""
    say "systemd user services are not available on this host, so the app was NOT started."
    say "Start it with:"
    say "  nohup $BIN_PATH -addr ${ADDR_HOST}:${PORT} -data $DATA_DIR -baseurl ${BASE_PATH} -no-browser >> $DATA_DIR/server.log 2>&1 &"
    say "Stop it with:"
    say "  pkill -f 'plex-file-manager -addr'"
else
    if have loginctl; then
        say ""
        say "Tip: so the service keeps running after you log out, run this yourself (needs sudo):"
        say "  sudo loginctl enable-linger ${USER:-$(whoami)}"
    fi
fi

# Reverse-proxy step (proxy bind mode only).
if [ "$BIND" = "proxy" ]; then
    if ultra_shaped; then
        write_proxy_snippet "$PORT"
        say "Wrote nginx proxy snippet -> $NGINX_SNIPPET (and restarted the proxy if available)"
    elif have nginx; then
        print_proxy_snippet "$PORT"
    fi
fi

STARTED_VERSION=""
if [ "$MANUAL_START" != "1" ]; then
    if ! STARTED_VERSION="$(wait_for_answer "$PORT" 30)"; then
        warn "the service started but the app did not answer on port $PORT within 30s."
        warn "Check the log: $DATA_DIR/server.log  (tail -50 $DATA_DIR/server.log)"
        exit 1
    fi
fi

say ""
say "=== Summary ==="
say "Mode: installed"
say "Version: $PKG_VERSION"
if [ "$MANUAL_START" = "1" ]; then
    say "Service: manual start (no systemd) — use the nohup line above"
else
    say "Service: $SERVICE (systemd --user), version answering: $STARTED_VERSION"
fi
say "Local check: http://127.0.0.1:${PORT}${BASE_PATH}"
if [ -n "$PUBLIC_URL" ]; then
    say "Public URL: $PUBLIC_URL"
elif [ "$BIND" = "lan" ]; then
    say "Open it at: http://<this-machine's-LAN-address>:${PORT}${BASE_PATH}"
fi
say "Bind mode: $BIND (${ADDR_HOST}:${PORT})"
say "ffmpeg: $FFMPEG_STATUS"
say "Next step: open the URL, add your library folders in Library roots, Start scan."
