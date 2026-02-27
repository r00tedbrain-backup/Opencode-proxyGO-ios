# OpenCode Remote Proxy

Access [OpenCode](https://opencode.ai) from your iPhone or any device — remotely and securely.

This is a lightweight Go reverse proxy that connects to the OpenCode desktop app running on your Mac and exposes it over your local network or [Tailscale VPN](https://tailscale.com), so you can use OpenCode's full web UI from your phone, tablet, or any browser.

> **You see the exact same sessions, projects, and chat history as on the desktop app — it's the same instance, not a copy.**

## Table of Contents

- [How It Works](#how-it-works)
- [Features](#features)
- [Requirements](#requirements)
- [Installation](#installation)
  - [Automated Install](#option-1-automated-install)
  - [Manual Install](#option-2-manual-install)
- [Accessing from Your Phone](#accessing-from-your-phone)
  - [Same WiFi (LAN)](#same-wifi-lan)
  - [From Anywhere (Tailscale)](#from-anywhere-tailscale)
  - [Add to Home Screen (PWA)](#add-to-home-screen-pwa)
- [Tailscale Setup Guide](#tailscale-setup-guide)
- [macOS Firewall Configuration](#macos-firewall-configuration)
- [Configuration Reference](#configuration-reference)
- [Useful Commands](#useful-commands)
- [Troubleshooting](#troubleshooting)
- [Uninstall](#uninstall)
- [Security Notes](#security-notes)
- [How Auto-Detection Works](#how-auto-detection-works)
- [License](#license)

## How It Works

```
iPhone/iPad          Tailscale VPN / LAN          Mac
┌──────────┐        ┌──────────────────┐        ┌─────────────────┐
│  Safari   │──────▶│  Reverse Proxy   │──────▶│  OpenCode App   │
│  :4096    │  Auth │  (Go binary)     │ Auto  │  (localhost:*)   │
└──────────┘        └──────────────────┘ detect └─────────────────┘
```

1. The OpenCode desktop app runs an internal HTTP server on `127.0.0.1` with a random port and auto-generated password
2. The proxy **auto-detects** the desktop server (port + credentials) — no manual configuration needed
3. You authenticate with **your own credentials** at the proxy
4. The proxy forwards requests to the desktop app, replacing the auth headers on-the-fly
5. You see the **exact same sessions and chat history** as on the desktop
6. If OpenCode restarts or changes port, the proxy **re-detects automatically** every 10 seconds
7. The proxy **never exits** — if OpenCode is not running, it returns 503 and waits for it to start

## Features

- **Zero config** — auto-detects OpenCode desktop server (port, password)
- **Never exits** — if OpenCode is not running, returns 503 and polls every 10s until it starts
- **Survives restarts** — re-detects if OpenCode changes port/password automatically
- **Secure** — credentials from environment variables, never hardcoded in binary
- **Rate limiting** — 20 failed login attempts = 15 minute IP ban (browser auto-requests excluded)
- **Interface-bound** — listens only on localhost + Tailscale + LAN (not `0.0.0.0`)
- **Tailscale auto-bind** — if Tailscale connects after proxy starts, it binds automatically
- **Autostart** — launchd service starts the proxy at login
- **Single binary** — no runtime dependencies, compiles to ~6MB native binary
- **Security headers** — X-Content-Type-Options, X-Frame-Options, Referrer-Policy
- **PWA support** — OpenCode's web UI works as a home screen app on iOS

## Requirements

- **macOS** (tested on Apple Silicon, should work on Intel too)
- **[OpenCode desktop app](https://opencode.ai)** installed and running
- **[Go 1.21+](https://go.dev/dl/)** (only needed for building)
- **[Tailscale](https://tailscale.com)** (optional, recommended for remote access outside your LAN)

## Installation

### Option 1: Automated Install

```bash
git clone https://github.com/r00tedbrain-backup/Opencode-proxyGO-ios.git
cd Opencode-proxyGO-ios
./install.sh
```

The installer will:
1. Check that Go is installed
2. Build the Go binary
3. Ask for your proxy username and password
4. Generate and install the launchd plist (autostart service)
5. Start the service immediately

### Option 2: Manual Install

#### 1. Clone and build

```bash
git clone https://github.com/r00tedbrain-backup/Opencode-proxyGO-ios.git
cd Opencode-proxyGO-ios/proxy-go
CGO_ENABLED=0 go build -ldflags="-s -w" -o ../opencode-remote-proxy .
```

#### 2. Run manually

```bash
export OPENCODE_PROXY_USER='your_username'
export OPENCODE_PROXY_PASS='your_password'
./opencode-remote-proxy
```

You should see output like:

```
  =============================================
    OpenCode Remote Proxy — Secured
  =============================================

  Desktop:    localhost:58062

  Localhost:  http://localhost:4096
  Tailscale:  http://100.x.y.z:4096
  WiFi (LAN): http://192.168.1.x:4096

  Credentials: loaded from environment variables
  Rate limit:  20 failed attempts -> 15m0s ban
  Poll:        every 10s
  Listening:   127.0.0.1 + 100.x.y.z + 192.168.1.x (not 0.0.0.0)
```

#### 3. Set up autostart (optional)

Copy the example plist and edit it with your paths and credentials:

```bash
cp com.opencode.remote-proxy.plist.example ~/Library/LaunchAgents/com.opencode.remote-proxy.plist
```

Edit the file — you need to change three things:

```bash
nano ~/Library/LaunchAgents/com.opencode.remote-proxy.plist
```

1. **Binary path:** Replace `/path/to/opencode-remote-proxy` with the full path to your binary
2. **Username:** Replace `your_username` with your desired username
3. **Password:** Replace `your_strong_password` with your desired password

> **Important:** If your password contains special XML characters, escape them:
> - `&` → `&amp;`
> - `<` → `&lt;`
> - `>` → `&gt;`
> - `"` → `&quot;`

Then load the service:

```bash
launchctl load ~/Library/LaunchAgents/com.opencode.remote-proxy.plist
```

The proxy will now start automatically every time you log in.

## Accessing from Your Phone

### Same WiFi (LAN)

If your phone and Mac are on the same WiFi network:

1. Find your Mac's local IP:
   ```bash
   ipconfig getifaddr en0
   ```
   Example output: `192.168.1.38`

2. Open Safari on your phone and go to:
   ```
   http://192.168.1.38:4096
   ```

3. Enter your proxy username and password when prompted

> **Note:** Your LAN IP may change if you switch networks or your router assigns a new IP. The proxy automatically detects the current LAN IP at startup.

### From Anywhere (Tailscale)

To access OpenCode from outside your home network (coffee shop, office, mobile data):

1. Set up Tailscale on both devices (see [Tailscale Setup Guide](#tailscale-setup-guide) below)

2. Find your Mac's Tailscale IP:
   ```bash
   /Applications/Tailscale.app/Contents/MacOS/Tailscale ip -4
   ```
   Example output: `100.127.79.99`

3. Open Safari on your phone and go to:
   ```
   http://100.127.79.99:4096
   ```

4. Enter your proxy username and password when prompted

> **Why no HTTPS?** Tailscale encrypts all traffic end-to-end with WireGuard. Adding HTTPS on top would be redundant — your connection is already fully encrypted.

### Add to Home Screen (PWA)

OpenCode's web UI has built-in PWA (Progressive Web App) support. You can add it to your iPhone home screen for a native app-like experience:

1. Open `http://<your-ip>:4096` in **Safari** on your iPhone
2. Log in with your credentials
3. Tap the **Share button** (square with arrow at the bottom)
4. Scroll down and tap **"Add to Home Screen"**
5. Name it "OpenCode" (or whatever you prefer) and tap **Add**

You'll now have an OpenCode icon on your home screen that opens in a full-screen browser view without Safari's address bar — it feels like a native app.

## Tailscale Setup Guide

[Tailscale](https://tailscale.com) is a free VPN that creates a secure private network between your devices. It uses WireGuard encryption and requires zero port forwarding or router configuration.

### 1. Create a Tailscale account

Go to [tailscale.com](https://tailscale.com) and sign up (free for personal use, up to 100 devices).

### 2. Install on your Mac

1. Download from [tailscale.com/download/mac](https://tailscale.com/download/mac) or the Mac App Store
2. Open Tailscale and sign in with your account
3. You'll get a Tailscale IP (like `100.x.y.z`)

Verify it's working:
```bash
/Applications/Tailscale.app/Contents/MacOS/Tailscale ip -4
```

### 3. Install on your iPhone

1. Download **Tailscale** from the [App Store](https://apps.apple.com/app/tailscale/id1470499037)
2. Open the app and sign in with the **same account** as your Mac
3. Enable the VPN when prompted

### 4. Verify connectivity

From your iPhone, open Safari and go to `http://<mac-tailscale-ip>:4096`. If you see a login prompt, everything is working.

> **Tip:** Tailscale IPs are stable — they don't change even if you switch WiFi networks or use mobile data. Bookmark your Tailscale URL for easy access.

## macOS Firewall Configuration

If your Mac firewall blocks incoming connections, you need to allow the proxy binary.

### If "Block all incoming connections" is OFF:

1. **System Settings** → **Network** → **Firewall** → **Options**
2. Click **+**
3. Navigate to the `opencode-remote-proxy` binary and select it
4. Set it to **Allow incoming connections**

### If "Block all incoming connections" is ON:

1. **System Settings** → **Network** → **Firewall** → **Options**
2. **Temporarily disable** "Block all incoming connections" (you need this to see the + button)
3. Click **+** and add the `opencode-remote-proxy` binary
4. Set it to **Allow incoming connections**
5. **Re-enable** "Block all incoming connections"

The proxy will now work even with the strictest firewall setting because it has an explicit allow rule.

> **Why not allow Node.js or Go runtime?** We compile to a standalone binary specifically so the firewall rule only applies to this one program — not to every Go or Node.js process on your system.

## Configuration Reference

### Environment Variables

| Variable | Description | Required |
|---|---|---|
| `OPENCODE_PROXY_USER` | Username for proxy authentication | Yes |
| `OPENCODE_PROXY_PASS` | Password for proxy authentication | Yes |

### Constants (in `proxy-go/main.go`)

| Constant | Default | Description |
|---|---|---|
| `ProxyPort` | `4096` | Port the proxy listens on |
| `MaxFailedAttempts` | `20` | Failed login attempts before IP ban |
| `BanDuration` | `15 * time.Minute` | Duration of IP ban after max failures |
| `PollInterval` | `10 * time.Second` | How often to check for OpenCode/Tailscale changes |

To change these, edit `proxy-go/main.go` and rebuild:

```bash
cd proxy-go
CGO_ENABLED=0 go build -ldflags="-s -w" -o ../opencode-remote-proxy .
```

Then restart the service:

```bash
launchctl unload ~/Library/LaunchAgents/com.opencode.remote-proxy.plist
launchctl load ~/Library/LaunchAgents/com.opencode.remote-proxy.plist
```

## Useful Commands

```bash
# View proxy output
cat /tmp/opencode-remote-proxy.log

# View error log
cat /tmp/opencode-remote-proxy-error.log

# Stop the service
launchctl unload ~/Library/LaunchAgents/com.opencode.remote-proxy.plist

# Start the service
launchctl load ~/Library/LaunchAgents/com.opencode.remote-proxy.plist

# Restart the service (stop + start)
launchctl unload ~/Library/LaunchAgents/com.opencode.remote-proxy.plist && \
launchctl load ~/Library/LaunchAgents/com.opencode.remote-proxy.plist

# Check if running
launchctl list | grep opencode

# Check what interfaces it's listening on
lsof -i :4096 -P -n | grep LISTEN

# Check your Mac's LAN IP
ipconfig getifaddr en0

# Check your Mac's Tailscale IP
/Applications/Tailscale.app/Contents/MacOS/Tailscale ip -4
```

## Troubleshooting

### "Too many failed attempts. Try again later."

Your IP has been temporarily banned after 20 failed login attempts. You have two options:

- **Wait 15 minutes** for the ban to expire automatically
- **Restart the proxy** to clear all bans immediately:
  ```bash
  launchctl unload ~/Library/LaunchAgents/com.opencode.remote-proxy.plist
  launchctl load ~/Library/LaunchAgents/com.opencode.remote-proxy.plist
  ```

**Common cause:** Special characters in your password (like `&`) may not be typed correctly by your phone's keyboard. Double-check your password carefully.

### Proxy starts but no listeners / can't connect

1. **Is OpenCode desktop running?** The proxy needs the OpenCode app to be open:
   ```bash
   ps -eo pid,command | grep "serve.*--port" | grep "opencode" | grep -v grep
   ```
   If no output, open the OpenCode app on your Mac.

2. **Is the port already in use?** Check if something else is using port 4096:
   ```bash
   lsof -i :4096 -P -n
   ```

3. **Check the error log:**
   ```bash
   cat /tmp/opencode-remote-proxy-error.log
   ```

### Can't connect from iPhone on same WiFi

1. **Check your Mac's IP:** `ipconfig getifaddr en0` — make sure you're using the right IP
2. **Check the firewall:** See [macOS Firewall Configuration](#macos-firewall-configuration)
3. **Check your phone:** Make sure your phone is on the same WiFi network, not on mobile data

### Can't connect via Tailscale

1. **Is Tailscale connected on both devices?** Open the Tailscale app on both Mac and iPhone and verify they're connected
2. **Can you ping the Mac from iPhone?** In the Tailscale app on iPhone, check if your Mac shows as "Connected"
3. **Is the proxy listening on the Tailscale IP?**
   ```bash
   lsof -i :4096 -P -n | grep LISTEN
   ```
   You should see a line with `100.x.y.z:4096`. If not, the Tailscale interface wasn't detected — restart the proxy.

### OpenCode shows different sessions than desktop

This means you're connecting to a different OpenCode instance. Make sure:
- You are NOT running `opencode web` or `opencode serve` separately
- The proxy is connecting to the desktop app's built-in server (check the log for the port number)
- You're in the right project directory on the desktop app

### Proxy stops working after OpenCode restart

The proxy never exits. If OpenCode stops, it returns 503 ("waiting for OpenCode to start") and auto-detects the new server within 10 seconds. If it doesn't recover:

```bash
# Check the error log
cat /tmp/opencode-remote-proxy-error.log

# If stuck, restart the proxy
launchctl unload ~/Library/LaunchAgents/com.opencode.remote-proxy.plist
launchctl load ~/Library/LaunchAgents/com.opencode.remote-proxy.plist
```

### Launchd service won't start

```bash
# Check the service status (exit code)
launchctl list | grep opencode

# A non-zero exit code means it crashed. Check logs:
cat /tmp/opencode-remote-proxy-error.log
cat /tmp/opencode-remote-proxy.log

# Common issues:
# - Credentials not set in plist
# - Binary path is wrong in plist
# - Password has unescaped & character (use &amp; in XML)
```

## Uninstall

### Remove the autostart service

```bash
# Stop and unload the service
launchctl unload ~/Library/LaunchAgents/com.opencode.remote-proxy.plist

# Remove the plist
rm ~/Library/LaunchAgents/com.opencode.remote-proxy.plist

# Remove log files
rm -f /tmp/opencode-remote-proxy.log /tmp/opencode-remote-proxy-error.log
```

### Remove the binary and source

```bash
# Remove the cloned repository (adjust path to where you cloned it)
rm -rf /path/to/opencode-remote-proxy
```

### Remove from macOS Firewall (optional)

1. **System Settings** → **Network** → **Firewall** → **Options**
2. Find `opencode-remote-proxy` in the list
3. Select it and click **-** to remove

## Security Notes

- Credentials are **never hardcoded** in the binary — always loaded from environment variables
- The binary binds to **specific interfaces only** (127.0.0.1, Tailscale IP, LAN IP) — never `0.0.0.0`
- Rate limiting prevents brute-force attacks (20 attempts = 15 min IP ban, browser auto-requests excluded)
- When using Tailscale, traffic is encrypted end-to-end with WireGuard
- The proxy password is **not** extractable from the binary via `strings`
- The compiled binary only needs a macOS firewall exception for itself — no runtime (Node/Go) is exposed
- Security headers are set on all responses (X-Content-Type-Options, X-Frame-Options, Referrer-Policy)
- The proxy connects to OpenCode's server only on `127.0.0.1` — it never exposes the internal server directly

## How Auto-Detection Works

The proxy finds the running OpenCode desktop app automatically:

1. Scans `ps` output for a process matching `opencode` + `serve --port`
2. Extracts the `--port` argument from the command line
3. Reads the `OPENCODE_SERVER_PASSWORD` from the process environment via `ps eww`
4. Connects to `127.0.0.1:<port>` with the extracted credentials

Every 10 seconds, the proxy:
- Re-checks the desktop server (handles OpenCode restarts, port changes, password changes)
- If OpenCode is not running, the proxy stays up and returns 503 — it **never exits**
- Cleans up expired rate-limit bans
- Attempts to bind the Tailscale interface if not yet listening (handles Tailscale connecting after proxy starts)

## License

MIT — see [LICENSE](LICENSE) file.
