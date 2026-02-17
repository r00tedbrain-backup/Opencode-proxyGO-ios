#!/bin/bash
set -e

# OpenCode Remote Proxy — Installer
# This script builds the proxy and sets up the launchd service for auto-start.

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BINARY_NAME="opencode-remote-proxy"
BINARY_PATH="$SCRIPT_DIR/$BINARY_NAME"
PLIST_NAME="com.opencode.remote-proxy.plist"
PLIST_DEST="$HOME/Library/LaunchAgents/$PLIST_NAME"

echo ""
echo "  ============================================="
echo "    OpenCode Remote Proxy — Installer"
echo "  ============================================="
echo ""

# --- Step 1: Check Go is installed ---
if ! command -v go &>/dev/null; then
    echo "  Error: Go is not installed."
    echo "  Install it from https://go.dev/dl/ and try again."
    exit 1
fi
echo "  [1/5] Go found: $(go version | awk '{print $3}')"

# --- Step 2: Build the binary ---
echo "  [2/5] Building proxy binary..."
cd "$SCRIPT_DIR/proxy-go"
CGO_ENABLED=0 go build -ldflags="-s -w" -o "$BINARY_PATH" .
chmod +x "$BINARY_PATH"
echo "        Built: $BINARY_PATH"

# --- Step 3: Prompt for credentials ---
echo ""
if [ -z "$OPENCODE_PROXY_USER" ]; then
    read -rp "  Enter your proxy username: " OPENCODE_PROXY_USER
fi
if [ -z "$OPENCODE_PROXY_PASS" ]; then
    read -rsp "  Enter your proxy password: " OPENCODE_PROXY_PASS
    echo ""
fi

if [ -z "$OPENCODE_PROXY_USER" ] || [ -z "$OPENCODE_PROXY_PASS" ]; then
    echo "  Error: Username and password are required."
    exit 1
fi

echo "  [3/5] Credentials configured."

# --- Step 4: Generate and install the launchd plist ---
# Escape special XML characters in the password
ESCAPED_PASS=$(echo "$OPENCODE_PROXY_PASS" | sed 's/&/\&amp;/g; s/</\&lt;/g; s/>/\&gt;/g; s/"/\&quot;/g')

# Unload old service if it exists
launchctl unload "$PLIST_DEST" 2>/dev/null || true

cat > "$PLIST_DEST" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.opencode.remote-proxy</string>
    <key>ProgramArguments</key>
    <array>
        <string>${BINARY_PATH}</string>
    </array>
    <key>EnvironmentVariables</key>
    <dict>
        <key>OPENCODE_PROXY_USER</key>
        <string>${OPENCODE_PROXY_USER}</string>
        <key>OPENCODE_PROXY_PASS</key>
        <string>${ESCAPED_PASS}</string>
    </dict>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <dict>
        <key>SuccessfulExit</key>
        <false/>
    </dict>
    <key>StandardOutPath</key>
    <string>/tmp/opencode-remote-proxy.log</string>
    <key>StandardErrorPath</key>
    <string>/tmp/opencode-remote-proxy-error.log</string>
    <key>ThrottleInterval</key>
    <integer>30</integer>
</dict>
</plist>
PLIST

echo "  [4/5] Launchd service installed: $PLIST_DEST"

# --- Step 5: Start the service ---
launchctl load "$PLIST_DEST"
sleep 2

if launchctl list | grep -q "com.opencode.remote-proxy"; then
    echo "  [5/5] Service started successfully!"
else
    echo "  [5/5] Warning: Service may not have started. Check logs:"
    echo "        cat /tmp/opencode-remote-proxy-error.log"
fi

echo ""
echo "  ============================================="
echo "    Installation complete!"
echo "  ============================================="
echo ""
echo "  The proxy will start automatically on login."
echo "  Make sure the OpenCode desktop app is running."
echo ""
echo "  Access from your phone via:"
echo "    - LAN:       http://<your-mac-ip>:4096"
echo "    - Tailscale: http://<your-tailscale-ip>:4096"
echo ""
echo "  Useful commands:"
echo "    View logs:     cat /tmp/opencode-remote-proxy.log"
echo "    View errors:   cat /tmp/opencode-remote-proxy-error.log"
echo "    Stop service:  launchctl unload ~/Library/LaunchAgents/$PLIST_NAME"
echo "    Start service: launchctl load ~/Library/LaunchAgents/$PLIST_NAME"
echo ""
