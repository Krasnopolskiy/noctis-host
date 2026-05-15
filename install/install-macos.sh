#!/usr/bin/env bash
# Install the native messaging manifest for noctis on macOS.
# Usage: install-macos.sh <chrome-extension-id>
set -euo pipefail

EXT_ID="${1:-}"
if [[ -z "$EXT_ID" ]]; then
  echo "Usage: $0 <extension-id>" >&2
  exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NATIVE_HOST_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
HOST_BIN_SRC="$NATIVE_HOST_DIR/noctis-host"

if [[ ! -x "$HOST_BIN_SRC" ]]; then
  echo "Helper binary missing or not executable: $HOST_BIN_SRC" >&2
  echo "Build first:  (cd native-host && go build -o noctis-host .)" >&2
  exit 1
fi

INSTALL_DIR="$HOME/.local/share/noctis"
mkdir -p "$INSTALL_DIR"
HOST_BIN_DEST="$INSTALL_DIR/noctis-host"
cp -f "$HOST_BIN_SRC" "$HOST_BIN_DEST"
chmod +x "$HOST_BIN_DEST"
xattr -d com.apple.quarantine "$HOST_BIN_DEST" 2>/dev/null || true

SINGBOX_SRC="$NATIVE_HOST_DIR/embed/sing-box"
if [[ -x "$SINGBOX_SRC" ]]; then
  cp -f "$SINGBOX_SRC" "$INSTALL_DIR/sing-box"
  chmod +x "$INSTALL_DIR/sing-box"
  xattr -d com.apple.quarantine "$INSTALL_DIR/sing-box" 2>/dev/null || true
  echo "copied sing-box -> $INSTALL_DIR/sing-box"
else
  echo "warn: native-host/embed/sing-box missing — run scripts/fetch-singbox.sh first" >&2
fi

NM_NAME="com.noctis.host"

# Each entry must point to <UserDataDir>/NativeMessagingHosts.
TARGETS=(
  "$HOME/Library/Application Support/Google/Chrome/NativeMessagingHosts"
  "$HOME/Library/Application Support/Google/Chrome Beta/NativeMessagingHosts"
  "$HOME/Library/Application Support/Google/Chrome Canary/NativeMessagingHosts"
  "$HOME/Library/Application Support/Chromium/NativeMessagingHosts"
  "$HOME/Library/Application Support/BraveSoftware/Brave-Browser/NativeMessagingHosts"
  "$HOME/Library/Application Support/Microsoft Edge/NativeMessagingHosts"
  "$HOME/Library/Application Support/Arc/User Data/NativeMessagingHosts"
)

written=0
for dir in "${TARGETS[@]}"; do
  parent="$(dirname "$dir")"
  if [[ ! -d "$parent" ]]; then
    continue
  fi
  mkdir -p "$dir"
  manifest="$dir/$NM_NAME.json"
  cat > "$manifest" <<JSON
{
  "name": "$NM_NAME",
  "description": "Noctis native helper",
  "path": "$HOST_BIN_DEST",
  "type": "stdio",
  "allowed_origins": ["chrome-extension://$EXT_ID/"]
}
JSON
  echo "wrote $manifest"
  written=$((written + 1))
done

if (( written == 0 )); then
  echo "No supported browser data dirs found." >&2
  exit 1
fi

echo "Done. Installed for $written browser(s)."
echo "Helper at: $HOST_BIN_DEST"
echo "Reload the unpacked extension in chrome://extensions to pick up changes."
