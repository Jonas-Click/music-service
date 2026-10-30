#!/bin/sh
# Install the matching release without replacing the user's configuration or songs.
set -eu

RELEASE_REPOSITORY="Jonas-Click/music-service"
RELEASE_TAG="latest"
if [ "$RELEASE_TAG" = latest ]; then
    RELEASE_URL="https://github.com/$RELEASE_REPOSITORY/releases/latest/download"
else
    RELEASE_URL="https://github.com/$RELEASE_REPOSITORY/releases/download/$RELEASE_TAG"
fi
RELEASE_URL="${MUSIC_RELEASE_URL:-$RELEASE_URL}"
ROOT="${MUSIC_ROOT:-}"
DATA="$ROOT/data"
INSTALL="$DATA/music-service"
BIN="$DATA/bin"
UNIT="$ROOT/etc/systemd/system/music-service.service"
PROFILE="$ROOT/etc/profile.d/music-path.sh"
OLD_PROFILE="$ROOT/etc/profile.d/music-alias.sh"
COMPLETION="$ROOT/etc/bash_completion.d/music"
SHORTCUT="$ROOT/usr/local/bin/music"
ONBOOT="$DATA/onboot.sh"
BEGIN='# >>> music-service >>>'
END='# <<< music-service <<<'
ASSETS='music-service install.sh music-completion.bash music-service.service config.json.example'
OFFLINE=""
MODE=install
PURGE=0
case "${1:-}" in
    --offline) [ "$#" -eq 2 ] || { echo 'Usage: install.sh --offline RELEASE_DIRECTORY' >&2; exit 1; }; OFFLINE="$2" ;;
    --uninstall|uninstall) MODE=uninstall; [ "${2:-}" != --purge ] || PURGE=1 ;;
    '') ;;
    *) echo 'Usage: install.sh [--offline RELEASE_DIRECTORY | --uninstall [--purge]]' >&2; exit 1 ;;
esac
[ -n "$ROOT" ] || [ "$(id -u)" = 0 ] || { echo 'Run this installer as root.' >&2; exit 1; }
command -v systemctl >/dev/null 2>&1 || { echo 'systemctl is required.' >&2; exit 1; }
command -v sha256sum >/dev/null 2>&1 || { echo 'sha256sum is required.' >&2; exit 1; }
mkdir -p "$DATA"
LOCK="$DATA/.music-service-install.lock"
mkdir "$LOCK" 2>/dev/null || { echo "Another installer is running, or a stale lock remains: $LOCK" >&2; exit 1; }
WORK=$(mktemp -d "$DATA/.music-stage.XXXXXX")
JOURNAL="$WORK/journal"
: > "$JOURNAL"
COUNT=0
CHANGED=0
SUCCESS=0
WAS_ACTIVE=0
WAS_ENABLED=0
systemctl is-active --quiet music-service 2>/dev/null && WAS_ACTIVE=1
systemctl is-enabled --quiet music-service 2>/dev/null && WAS_ENABLED=1

fail() { echo "music-service: $*" >&2; exit 1; }
remember() {
    COUNT=$((COUNT + 1))
    if [ -e "$1" ] || [ -L "$1" ]; then cp -pP "$1" "$WORK/old-$COUNT"; fi
    printf '%s|%s\n' "$COUNT" "$1" >> "$JOURNAL"
}
put() {
    [ ! -d "$2" ] || fail "Refusing to replace directory $2"
    remember "$2"
    mkdir -p "$(dirname "$2")"
    cp "$1" "$2.music-tmp-$$"
    chmod "$3" "$2.music-tmp-$$"
    mv -f "$2.music-tmp-$$" "$2"
}
link() {
    [ ! -d "$2" ] || fail "Refusing to replace directory $2"
    remember "$2"
    mkdir -p "$(dirname "$2")"
    ln -s "$1" "$2.music-tmp-$$"
    mv -f "$2.music-tmp-$$" "$2"
}
cleanup() {
    status=$?
    trap - EXIT HUP INT TERM
    if [ "$CHANGED" = 1 ] && [ "$SUCCESS" = 0 ]; then
        echo 'Installation failed; restoring the previous executable and setup.' >&2
        systemctl stop music-service 2>/dev/null || true
        awk '{ lines[NR]=$0 } END { for(i=NR;i>0;i--) print lines[i] }' "$JOURNAL" |
        while IFS='|' read -r number path; do
            rm -f "$path"
            if [ -e "$WORK/old-$number" ] || [ -L "$WORK/old-$number" ]; then
                cp -pP "$WORK/old-$number" "$path"
            fi
        done
        systemctl daemon-reload 2>/dev/null || true
        if [ "$WAS_ENABLED" = 0 ]; then systemctl disable music-service 2>/dev/null || true; fi
        if [ "$WAS_ACTIVE" = 1 ]; then systemctl start music-service 2>/dev/null || true; fi
    fi
    rm -rf "$WORK"
    rmdir "$LOCK"
    exit "$status"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

strip_block() {
    if [ -f "$ONBOOT" ]; then
        awk -v begin="$BEGIN" -v end="$END" '
            $0==begin { if (inside) exit 2; inside=1; next }
            $0==end { if (!inside) exit 2; inside=0; next }
            !inside { print }
            END { if (inside) exit 2 }
        ' "$ONBOOT" > "$WORK/onboot" || fail 'Unbalanced music-service markers in onboot.sh'
    else
        printf '#!/bin/sh\n' > "$WORK/onboot"
    fi
}
strip_block

if [ "$MODE" = uninstall ]; then
    systemctl stop music-service
    systemctl disable music-service
    rm -f "$UNIT" "$PROFILE" "$OLD_PROFILE" "$COMPLETION"
    if [ -L "$SHORTCUT" ] && [ "$(readlink "$SHORTCUT")" = "$BIN/music" ]; then rm -f "$SHORTCUT"; fi
    if [ -L "$BIN/music-service" ] && [ "$(readlink "$BIN/music-service")" = "$INSTALL/music-service" ]; then rm -f "$BIN/music-service"; fi
    if [ -f "$BIN/music" ] && grep -Fq "$INSTALL/music-service" "$BIN/music"; then rm -f "$BIN/music"; fi
    put "$WORK/onboot" "$ONBOOT" 755
    systemctl daemon-reload
    if [ "$PURGE" = 1 ]; then rm -rf "$INSTALL"; fi
    SUCCESS=1
    echo "Uninstalled. Retained backups and, unless --purge was specified, $INSTALL."
    exit 0
fi

# Downloads give up after 20 s without data instead of hanging: wget's defaults
# (15 minutes per stalled connection, 20 tries) once left the installer silent
# for over an hour. BusyBox wget has no -t.
if wget --help 2>&1 | grep -q BusyBox; then WGET_OPTS="-T 20"; else WGET_OPTS="-T 20 -t 3"; fi
CURL_OPTS="--connect-timeout 20 --speed-limit 1 --speed-time 20 --retry 2"
fetch() {
    if [ -n "$OFFLINE" ]; then
        cp "$OFFLINE/$1" "$WORK/$1"
        return
    fi
    echo "Downloading $1..."
    if command -v wget >/dev/null 2>&1; then
        wget -q $WGET_OPTS -O "$WORK/$1" "$RELEASE_URL/$1"
    elif command -v curl >/dev/null 2>&1; then
        curl -fsSL $CURL_OPTS -o "$WORK/$1" "$RELEASE_URL/$1"
    else
        fail 'wget or curl is required for an online install'
    fi
}
fetch SHA256SUMS || fail "Cannot fetch SHA256SUMS from $RELEASE_URL; check the internet connection or use --offline. No installed files changed."
for name in $ASSETS; do
    fetch "$name" || fail "Cannot fetch $name. No installed files changed."
    want=$(awk -v name="$name" '$2==name || $2=="*"name { n++; hash=$1 } END { if(n!=1) exit 1; print hash }' "$WORK/SHA256SUMS") || fail "Missing or duplicate checksum for $name"
    [ "${#want}" = 64 ] || fail "Invalid checksum for $name"
    case "$want" in *[!0-9a-fA-F]*) fail "Invalid checksum for $name" ;; esac
    got=$(sha256sum "$WORK/$name"); got=${got%% *}
    [ "$want" = "$got" ] || fail "Checksum mismatch for $name. No installed files changed."
done
chmod 755 "$WORK/music-service"
VERSION=$("$WORK/music-service" --version) || fail 'Release executable cannot run on this machine'
[ -n "$VERSION" ] || fail 'Release executable has no version'

# Existing installations can put their executable in either persistent directory.
# Keep the config independently of where that executable was installed.
if [ -e "$SHORTCUT" ] || [ -L "$SHORTCUT" ]; then
    if [ -L "$SHORTCUT" ]; then
        case "$(readlink "$SHORTCUT")" in "$BIN/music"|"$INSTALL/music-service") ;; *) fail "$SHORTCUT belongs to another installation" ;; esac
    else
        fail "$SHORTCUT is not the managed music shortcut"
    fi
fi
if [ -f "$UNIT" ]; then cp "$UNIT" "$WORK/unit";
elif [ -f "$INSTALL/music-service.service" ]; then cp "$INSTALL/music-service.service" "$WORK/unit";
else cp "$WORK/music-service.service" "$WORK/unit"; fi
# MUSIC_ROOT isolates filesystem integration tests; production paths stay standard.
if [ -n "$ROOT" ]; then
    sed -e "s|^ExecStart=/data/music-service/music-service|ExecStart=$INSTALL/music-service|" \
        -e "s|^ExecStart=/data/bin/music-service|ExecStart=$BIN/music-service|" "$WORK/unit" > "$WORK/unit-root"
    mv "$WORK/unit-root" "$WORK/unit"
fi

awk -F= -v canonical="$INSTALL/music-service" -v compat="$BIN/music-service" '
    $1=="ExecStart" { split($2, words, /[[:space:]]/); executable=words[1]; gsub(/"/, "", executable); if(executable==canonical || executable==compat) found=1 }
    END { exit !found }
' "$WORK/unit" || fail 'Existing service uses another executable path; review its unit before upgrading'

BACKUPS="$DATA/music-service-backups"
mkdir -p "$BACKUPS"
chmod 700 "$BACKUPS"
BACKUP=$(mktemp -d "$BACKUPS/install-XXXXXX")
if [ -d "$INSTALL" ]; then cp -pR "$INSTALL" "$BACKUP/installation"; fi
for path in "$UNIT" "$PROFILE" "$COMPLETION" "$ONBOOT" "$BIN/music" "$BIN/music-service"; do
    if [ -e "$path" ] || [ -L "$path" ]; then cp -pP "$path" "$BACKUP/$(printf '%s' "$path" | tr / _)"; fi
done
printf 'Installing %s; backup: %s\n' "$VERSION" "$BACKUP"
CHANGED=1
if [ "$WAS_ACTIVE" = 1 ]; then systemctl stop music-service; fi
mkdir -p "$INSTALL" "$BIN"
put "$WORK/music-service" "$INSTALL/music-service" 755
put "$WORK/install.sh" "$INSTALL/install.sh" 755
put "$WORK/music-completion.bash" "$INSTALL/music-completion.bash" 644
put "$WORK/unit" "$INSTALL/music-service.service" 644
if [ ! -f "$INSTALL/config.json" ]; then put "$WORK/config.json.example" "$INSTALL/config.json" 600; fi
printf '#!/bin/sh\nexec "%s/music-service" ctl "$@"\n' "$INSTALL" > "$WORK/music"
put "$WORK/music" "$BIN/music" 755
link "$INSTALL/music-service" "$BIN/music-service"
link "$BIN/music" "$SHORTCUT"
cat > "$WORK/profile" <<EOF
case ":\$PATH:" in
    *:$BIN:*) ;;
    *) PATH="\${PATH:+\$PATH:}$BIN"; export PATH ;;
esac
EOF
put "$WORK/profile" "$INSTALL/music-path.sh" 644
put "$WORK/profile" "$PROFILE" 644
put "$WORK/music-completion.bash" "$COMPLETION" 644
put "$WORK/unit" "$UNIT" 644

cat > "$WORK/restore.sh" <<EOF
#!/bin/sh
set -e
mkdir -p "$(dirname "$UNIT")" "$(dirname "$PROFILE")" "$(dirname "$COMPLETION")" "$(dirname "$SHORTCUT")"
cp "$INSTALL/music-service.service" "$UNIT"
cp "$INSTALL/music-path.sh" "$PROFILE"
cp "$INSTALL/music-completion.bash" "$COMPLETION"
if [ ! -e "$SHORTCUT" ] && [ ! -L "$SHORTCUT" ]; then ln -s "$BIN/music" "$SHORTCUT"; fi
systemctl daemon-reload
systemctl is-enabled --quiet music-service 2>/dev/null || systemctl enable music-service
systemctl is-active --quiet music-service 2>/dev/null || systemctl --no-block start music-service
EOF
put "$WORK/restore.sh" "$INSTALL/restore.sh" 755
printf '\n%s\nsh "%s/restore.sh"\n%s\n' "$BEGIN" "$INSTALL" "$END" >> "$WORK/onboot"
put "$WORK/onboot" "$ONBOOT" 755
systemctl daemon-reload
systemctl enable music-service
systemctl start music-service
sleep 2
systemctl is-active --quiet music-service || fail 'Service did not start; check journalctl -u music-service'
SUCCESS=1
# Alias from releases before the music command moved to /data/bin.
rm -f "$OLD_PROFILE" "$INSTALL/music-alias.sh"
# Each backup holds a full copy of the installation; keep the three newest.
ls -1dt "$BACKUPS"/install-* 2>/dev/null | tail -n +4 | while IFS= read -r old; do rm -rf "$old"; done
printf 'Installed %s. Use music help in SSH (no new login required).\n' "$VERSION"
printf 'Existing config and songs were kept. Change settings with music settings.\n'
printf 'This installs the MDB service only, not the VESC app or firmware.\n'
