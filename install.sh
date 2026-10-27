#!/bin/sh
# Tunu Music Service — guided installer
#
# Online install (one line on the scooter terminal):
#   wget -qO /tmp/music-install.sh \
#        https://raw.githubusercontent.com/Jonas-Click/music-service/main/install.sh \
#     && sh /tmp/music-install.sh
#
# Uninstall:  sh /data/music-service/install.sh --uninstall [--purge]
#
# Offline install — copy all release files into /data/music-service/ first,
# then run:  sh /data/music-service/install.sh

set -e

RELEASE_URL="https://github.com/Jonas-Click/music-service/releases/latest/download"
INSTALL="/data/music-service"
SERVICE_FILE="/etc/systemd/system/music-service.service"
PROFILE_FILE="/etc/profile.d/music-path.sh"
# Before 1.1.0 the command was a shell alias in this file.
OLD_PROFILE_FILE="/etc/profile.d/music-alias.sh"
# /data/bin survives OS updates and is on PATH in login shells.
BIN_DIR="/data/bin"
MUSIC_BIN="$BIN_DIR/music"
COMPLETION_DIR="/etc/bash_completion.d"
ONBOOT="/data/onboot.sh"
ONBOOT_BEGIN="# >>> music-service >>>"
ONBOOT_END="# <<< music-service <<<"
PROFILE_SRC="$INSTALL/music-path.sh"
COMPLETION_SRC="$INSTALL/music-completion.bash"
SERVICE_SRC="$INSTALL/music-service.service"

# ── Helpers ──────────────────────────────────────────────────────────────────

ok()   { printf '  \033[32m\xe2\x9c\x93\033[0m  %s\n' "$1"; }
info() { printf '  \033[34m\xc2\xb7\033[0m  %s\n' "$1"; }
warn() { printf '  \033[33m!\033[0m  %s\n' "$1"; }
fail() { printf '  \033[31m\xe2\x9c\x97\033[0m  %s\n' "$1"; exit 1; }

section() { printf '\n\033[1m\xe2\x94\x80\xe2\x94\x80 %s \xe2\x94\x80\xe2\x94\x80\xe2\x94\x80\xe2\x94\x80\xe2\x94\x80\xe2\x94\x80\xe2\x94\x80\xe2\x94\x80\xe2\x94\x80\xe2\x94\x80\xe2\x94\x80\xe2\x94\x80\xe2\x94\x80\xe2\x94\x80\xe2\x94\x80\xe2\x94\x80\xe2\x94\x80\xe2\x94\x80\033[0m\n\n' "$1"; }

# Prompts fall back to defaults when there's no terminal to ask: a
# non-interactive ssh run must not hang forever on the first question.
interactive() {
    [ "${MUSIC_NONINTERACTIVE:-0}" != "1" ] && [ -r /dev/tty ] && [ -t 0 ]
}

ask() {
    if ! interactive; then
        REPLY="$2"
        printf '  %s [%s] (no terminal, using default)\n' "$1" "$2"
        return 0
    fi
    printf '  %s [%s]: ' "$1" "$2"
    read REPLY </dev/tty || REPLY="$2"
    REPLY="${REPLY:-$2}"
}

ask_yn() {
    ask "$1" "$2"
    case "$REPLY" in [Yy]*) return 0;; *) return 1;; esac
}

ALLOW_INSECURE="${MUSIC_ALLOW_INSECURE:-0}"

download() {
    _url="$1"; _dst="$2"
    if command -v wget >/dev/null 2>&1; then
        wget -qO "$_dst" "$_url" && return 0
        [ "$ALLOW_INSECURE" = "1" ] || return 1
        wget --no-check-certificate -qO "$_dst" "$_url"
    elif command -v curl >/dev/null 2>&1; then
        curl -fsSL -o "$_dst" "$_url" && return 0
        [ "$ALLOW_INSECURE" = "1" ] || return 1
        curl -fsSLk -o "$_dst" "$_url"
    else
        return 1
    fi
}

SUMS_FILE=""

fetch_sums() {
    SUMS_FILE="$(mktemp 2>/dev/null || echo /tmp/music-sha256sums)"
    if download "$RELEASE_URL/SHA256SUMS" "$SUMS_FILE" 2>/dev/null && [ -s "$SUMS_FILE" ]; then
        ok "SHA256SUMS fetched ($(grep -c . "$SUMS_FILE") entries)"
    else
        rm -f "$SUMS_FILE"; SUMS_FILE=""
        warn "SHA256SUMS unavailable — downloads cannot be verified"
    fi
}

verify_file() {
    _path="$1"; _name="$2"
    [ -n "$SUMS_FILE" ] || return 0
    command -v sha256sum >/dev/null 2>&1 || { warn "no sha256sum on this system — skipping verification"; return 0; }
    _want="$(awk -v n="$_name" '$2 == n || $2 == "*" n { print $1; exit }' "$SUMS_FILE")"
    if [ -z "$_want" ]; then
        rm -f "$_path"
        fail "$_name is not listed in SHA256SUMS — refusing to install it"
    fi
    _got="$(sha256sum "$_path" | cut -d" " -f1)"
    if [ "$_want" != "$_got" ]; then
        rm -f "$_path"
        fail "$_name failed checksum verification (expected $_want, got $_got)"
    fi
    ok "$_name verified"
    return 0
}

# ── Uninstall ────────────────────────────────────────────────────────────────

if [ "${1:-}" = "--uninstall" ] || [ "${1:-}" = "uninstall" ]; then
    section "Uninstall"

    info "Stopping the service..."
    systemctl stop music-service 2>/dev/null || true
    systemctl disable music-service 2>/dev/null || true
    ok "service stopped and disabled"

    info "Removing files installed outside $INSTALL..."
    rm -f "$SERVICE_FILE" "$PROFILE_FILE" "$OLD_PROFILE_FILE" "$COMPLETION_DIR/music"
    # Only our own wrapper: never a music command someone else put there.
    if grep -qF "$INSTALL/music-service ctl" "$MUSIC_BIN" 2>/dev/null; then
        rm -f "$MUSIC_BIN"
    fi
    systemctl daemon-reload 2>/dev/null || true
    ok "unit, music command and completion removed"

    if [ -f "$ONBOOT" ]; then
        cp "$ONBOOT" "$ONBOOT.pre-uninstall"
        if grep -qF "$ONBOOT_BEGIN" "$ONBOOT.pre-uninstall"; then
            sed "/^$ONBOOT_BEGIN\$/,/^$ONBOOT_END\$/d" "$ONBOOT.pre-uninstall" > "$ONBOOT"
        else
            grep -v "music-service" "$ONBOOT.pre-uninstall" > "$ONBOOT" || true
        fi
        chmod +x "$ONBOOT"
        ok "onboot.sh entries removed (backup: $ONBOOT.pre-uninstall)"
    fi

    if [ "${2:-}" = "--purge" ] || ask_yn "Also delete $INSTALL (config.json)?" "n"; then
        rm -rf "$INSTALL"
        ok "$INSTALL deleted"
    else
        info "$INSTALL kept — re-run install.sh to reinstall from it"
    fi

    printf '\n  Uninstalled.\n\n'
    exit 0
fi

# ── Banner ───────────────────────────────────────────────────────────────────

printf '\033[2J\033[H'
cat << 'BANNER'

  Tunu Music Service — Guided Installer
  github.com/Jonas-Click/music-service

  Install path: /data/music-service/
  Run this installer again at any time to upgrade
  or change your configuration.

BANNER

# ── System check ─────────────────────────────────────────────────────────────

section "System check"

command -v systemctl >/dev/null 2>&1 || fail "systemctl not found — is this the scooter's MDB?"
ok "systemd present"

if [ ! -e /dev/can0 ] && ! ip link show can0 >/dev/null 2>&1; then
    warn "no can0 interface found yet — the service retries CAN and Redis"
    warn "connections on its own, so this is not fatal, but playback will do"
    warn "nothing until can0 is up."
fi

UPGRADE=0
if [ -d "$INSTALL" ] && [ -f "$INSTALL/music-service" ]; then
    CURRENT_VER=$("$INSTALL/music-service" --version 2>/dev/null || echo "unknown")
    warn "Existing install found ($CURRENT_VER)"
    ask_yn "Upgrade / reconfigure?" "y" || { echo; echo "  Aborted."; exit 0; }
    UPGRADE=1
else
    info "Fresh install -> $INSTALL"
fi

# ── Download ─────────────────────────────────────────────────────────────────

section "Files"

mkdir -p "$INSTALL"
mkdir -p "$COMPLETION_DIR"

fetch_sums

info "music-service binary..."
if download "$RELEASE_URL/music-service" "$INSTALL/music-service.tmp" 2>/dev/null; then
    verify_file "$INSTALL/music-service.tmp" "music-service"
    mv "$INSTALL/music-service.tmp" "$INSTALL/music-service"
    ok "music-service (downloaded)"
elif [ -f "$INSTALL/music-service" ]; then
    rm -f "$INSTALL/music-service.tmp"
    ok "music-service (already present — skipped download)"
else
    rm -f "$INSTALL/music-service.tmp"
    fail "No music-service binary found. Copy it to $INSTALL/ first."
fi
chmod +x "$INSTALL/music-service"

info "Bash tab-completion..."
if download "$RELEASE_URL/music-completion.bash" "$COMPLETION_SRC.tmp" 2>/dev/null; then
    mv "$COMPLETION_SRC.tmp" "$COMPLETION_SRC"
    ok "bash completion (downloaded)"
elif [ -f "$COMPLETION_SRC" ]; then
    rm -f "$COMPLETION_SRC.tmp"
    ok "bash completion (already present — skipped download)"
else
    rm -f "$COMPLETION_SRC.tmp"
    warn "Completion not available — tab completion will not work."
fi

# ── Configuration ─────────────────────────────────────────────────────────────

section "Configuration"

if [ "$UPGRADE" -eq 1 ] && [ -f "$INSTALL/config.json" ]; then
    echo "  Existing config.json found."
    echo
    echo "  (k) Keep existing config — no changes"
    echo "  (c) Reconfigure interactively"
    echo "  (n) Open config.json in nano to edit manually"
    echo
    ask "Choice" "k"
    case "$REPLY" in
        [Cc]*) CONFIG_MODE="configure" ;;
        [Nn]*) CONFIG_MODE="nano" ;;
        *)     CONFIG_MODE="keep" ;;
    esac
else
    echo "  Configure the unlock melody now, or use defaults (disabled) and"
    echo "  edit config.json later?"
    echo
    echo "  (c) Configure interactively"
    echo "  (d) Use defaults — edit /data/music-service/config.json with nano later"
    echo
    ask "Choice" "d"
    case "$REPLY" in
        [Cc]*) CONFIG_MODE="configure" ;;
        *)     CONFIG_MODE="defaults" ;;
    esac
fi

if [ "$CONFIG_MODE" = "nano" ]; then
    nano "$INSTALL/config.json" </dev/tty
    ok "config.json edited"
    WRITE_CONFIG=0

elif [ "$CONFIG_MODE" = "keep" ]; then
    ok "config.json kept unchanged"
    WRITE_CONFIG=0

elif [ "$CONFIG_MODE" = "defaults" ]; then
    CAN_IFACE="can0"
    UNLOCK_ENABLED="false"
    UNLOCK_SONG=3
    UNLOCK_VOLUME=100
    WRITE_CONFIG=1

else  # configure
    echo
    ask "CAN interface" "can0"
    CAN_IFACE="$REPLY"

    echo
    echo "  Play a melody automatically when the vehicle unlocks (by default,"
    echo "  when vehicle state transitions to 'parked' — see README.md)."
    echo
    if ask_yn "Enable unlock melody?" "n"; then
        UNLOCK_ENABLED="true"
        echo
        info "Songs: 2=La Cucaracha  3=Korobeiniki  4=Happy Birthday to You"
        info "Run \"music songs\" any time for the current list, or"
        info "\"music songs add\" to add your own (see README.md)."
        echo
        ask "Song number" "3"
        UNLOCK_SONG="$REPLY"
        ask "Volume to set first (0-100, 0 = leave as-is)" "100"
        UNLOCK_VOLUME="$REPLY"
    else
        UNLOCK_ENABLED="false"
        UNLOCK_SONG=3
        UNLOCK_VOLUME=100
    fi
    WRITE_CONFIG=1
fi

# ── Install ───────────────────────────────────────────────────────────────────

section "Installing"

if [ "$WRITE_CONFIG" -eq 1 ]; then
    cat > "$INSTALL/config.json" << EOF
{
  "can": { "interface": "$CAN_IFACE" },
  "redis": { "addr": "127.0.0.1:6379" },
  "status": { "stale_seconds": 5 },
  "unlock_melody": {
    "enabled": $UNLOCK_ENABLED,
    "song": $UNLOCK_SONG,
    "volume": $UNLOCK_VOLUME,
    "duration_seconds": 15,
    "trigger_field": "state",
    "trigger_value": "parked",
    "trigger_from": "stand-by"
  }
}
EOF
    ok "config.json written"
fi

cat > "$SERVICE_SRC" << 'EOF'
[Unit]
Description=Tunu (VESC) Music Playback Service
After=librescoot-onboot.service valkey.service redis.service network.target
StartLimitIntervalSec=300
StartLimitBurst=5

[Service]
Type=simple
ExecStart=/data/music-service/music-service
Restart=on-failure
RestartSec=10
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF
cp "$SERVICE_SRC" "$SERVICE_FILE"
ok "systemd service -> $SERVICE_FILE"

# A real command rather than an alias, so it also works in shells that load no
# profile: the Sunshine web shell, sh -c, cron, scripts.
mkdir -p "$BIN_DIR"
cat > "$MUSIC_BIN.tmp" << EOF
#!/bin/sh
# Installed by music-service install.sh.
exec $INSTALL/music-service ctl "\$@"
EOF
chmod 755 "$MUSIC_BIN.tmp"
mv "$MUSIC_BIN.tmp" "$MUSIC_BIN"
ok "music command -> $MUSIC_BIN"

# Older system images don't put /data/bin on PATH themselves.
cat > "$PROFILE_SRC" << EOF
# Installed by music-service install.sh: /data/bin holds the music command.
case ":\$PATH:" in
    *:$BIN_DIR:*) ;;
    *) PATH="\${PATH:+\$PATH:}$BIN_DIR"; export PATH ;;
esac
EOF
mkdir -p "$(dirname "$PROFILE_FILE")"
cp "$PROFILE_SRC" "$PROFILE_FILE"
rm -f "$OLD_PROFILE_FILE" "$INSTALL/music-alias.sh"
ok "/data/bin on PATH -> $PROFILE_FILE"

if [ -f "$COMPLETION_SRC" ]; then
    cp "$COMPLETION_SRC" "$COMPLETION_DIR/music"
fi

# ── onboot.sh integration ────────────────────────────────────────────────────

write_onboot_block() {
    {
        printf '%s\n' "$ONBOOT_BEGIN"
        printf '# Managed by music-service install.sh — anything between these markers is\n'
        printf '# replaced on the next install and removed by --uninstall.\n'
        printf 'mkdir -p %s\n' "$COMPLETION_DIR"
        printf 'cmp -s %s %s || cp %s %s\n' "$PROFILE_SRC" "$PROFILE_FILE" "$PROFILE_SRC" "$PROFILE_FILE"
        printf 'cmp -s %s %s/music || cp %s %s/music\n' "$COMPLETION_SRC" "$COMPLETION_DIR" "$COMPLETION_SRC" "$COMPLETION_DIR"
        printf '\n'
        printf 'if ! cmp -s %s %s; then\n' "$SERVICE_SRC" "$SERVICE_FILE"
        printf '    cp %s %s\n' "$SERVICE_SRC" "$SERVICE_FILE"
        printf '    systemctl daemon-reload\n'
        printf 'fi\n'
        printf 'systemctl is-enabled --quiet music-service 2>/dev/null || systemctl enable music-service\n'
        printf '# --no-block: this script *is* librescoot-onboot.service; a blocking start\n'
        printf '# would wait on the script starting it.\n'
        printf 'systemctl is-active --quiet music-service 2>/dev/null || systemctl --no-block start music-service\n'
        printf '%s\n' "$ONBOOT_END"
    } >> "$ONBOOT"
}

ensure_shebang() {
    head -1 "$ONBOOT" 2>/dev/null | grep -q '^#!' && return 0
    { printf '#!/bin/sh\n'; cat "$ONBOOT" 2>/dev/null; } > "$ONBOOT.tmp"
    mv "$ONBOOT.tmp" "$ONBOOT"
}

if [ ! -f "$ONBOOT" ]; then
    printf '#!/bin/sh\n' > "$ONBOOT"
    write_onboot_block
    ok "onboot.sh — created"
elif grep -qF "$ONBOOT_BEGIN" "$ONBOOT"; then
    cp "$ONBOOT" "$ONBOOT.bak"
    sed "/^$ONBOOT_BEGIN\$/,/^$ONBOOT_END\$/d" "$ONBOOT.bak" > "$ONBOOT"
    ensure_shebang
    write_onboot_block
    ok "onboot.sh — managed block refreshed (backup: $ONBOOT.bak)"
else
    write_onboot_block
    ok "onboot.sh — managed block appended"
fi
chmod +x "$ONBOOT"

# ── Start service ─────────────────────────────────────────────────────────────

section "Starting"

systemctl daemon-reload
if systemctl is-active --quiet music-service 2>/dev/null; then
    systemctl restart music-service
else
    systemctl enable --now music-service 2>/dev/null || true
fi

sleep 1

if systemctl is-active --quiet music-service 2>/dev/null; then
    ok "music-service is running"
else
    warn "music-service did not start — check: journalctl -u music-service -n 20"
fi

# ── Done ──────────────────────────────────────────────────────────────────────

cat << 'EOF'

  Install complete!

  The music command is in /data/bin. Open a new shell if "music" isn't
  found yet. In a shell that loads no profile (the Sunshine web shell),
  use:  sh -lc "music status"

  Quick start:
    music play 2          play La Cucaracha
    music vol 50           half volume
    music stop
    music status
    music help              full command reference

  Edit config manually:
    nano /data/music-service/config.json
    systemctl restart music-service

  Run this installer again to upgrade or reconfigure:
    sh /data/music-service/install.sh

  Service logs:
    journalctl -u music-service -f

EOF
