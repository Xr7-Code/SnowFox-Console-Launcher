#!/bin/bash
# SnowFoxOS Console Launcher — start.sh (Optimiert für i3)

LAUNCHER_DIR="$(cd "$(dirname "$0")" && pwd)"
BINARY="$LAUNCHER_DIR/snowfox_launcher"

# ─── Abhängigkeiten prüfen ────────────────────────────────────────────────────
check_pkg() {
    if ! dpkg -s "$1" > /dev/null 2>&1; then
        echo "[SnowFox] Fehlende Abhängigkeit: $1"
        echo "          Installieren mit: sudo apt install $1"
        MISSING=1
    fi
}
MISSING=0
check_pkg libevdev-dev
check_pkg libudev-dev
check_pkg libwayland-dev
check_pkg libx11-dev
check_pkg libxkbcommon-dev
check_pkg libgl1-mesa-dev
check_pkg pkg-config
[ $MISSING -eq 1 ] && exit 1

# ─── Bauen falls nötig ────────────────────────────────────────────────────────
NEEDS_BUILD=0
[ ! -f "$BINARY" ] && NEEDS_BUILD=1
for src in "$LAUNCHER_DIR"/*.go; do
    [ "$src" -nt "$BINARY" ] && NEEDS_BUILD=1
done

if [ $NEEDS_BUILD -eq 1 ]; then
    echo "[SnowFox] Baue snowfox_launcher..."
    cd "$LAUNCHER_DIR"
    /usr/local/go/bin/go mod tidy && /usr/local/go/bin/go build -o "$BINARY" . \
        && echo "[SnowFox] Build OK." \
        || { echo "[SnowFox] Build fehlgeschlagen!"; exit 1; }
fi

# ─── Workspace 8 vorbereiten ──────────────────────────────────────────────────
# Sicherstellen dass Workspace 8 existiert und leer für den Launcher ist
i3-msg "workspace 8" > /dev/null 2>&1

# ─── Starten ──────────────────────────────────────────────────────────────────
pkill -f snowfox_launcher > /dev/null 2>&1
sleep 0.2

echo "[SnowFox] Starte Launcher auf Workspace 8..."

# Launcher starten — i3 weist das Fenster dem aktuellen Workspace zu (8)
# exec ersetzt die Shell damit Signale sauber weitergeleitet werden
exec "$BINARY" -games "$LAUNCHER_DIR/games.json"