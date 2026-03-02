package main

import (
	"encoding/base64"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

// === CONFIGURATION ===
const (
	ProxyPort = "4096"

	// Max failed auth attempts before temporary ban
	MaxFailedAttempts = 20
	BanDuration       = 15 * time.Minute

	// How often to check for OpenCode desktop and Tailscale changes
	PollInterval = 10 * time.Second
)

// DesktopTarget holds the detected OpenCode desktop server info
type DesktopTarget struct {
	Host     string
	Port     string
	User     string
	Password string
}

// === RATE LIMITER ===
type rateLimiter struct {
	mu       sync.Mutex
	attempts map[string]*attemptInfo
}

type attemptInfo struct {
	count    int
	bannedAt time.Time
}

var limiter = &rateLimiter{
	attempts: make(map[string]*attemptInfo),
}

func (rl *rateLimiter) isBlocked(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	info, exists := rl.attempts[ip]
	if !exists {
		return false
	}

	if !info.bannedAt.IsZero() && time.Since(info.bannedAt) > BanDuration {
		delete(rl.attempts, ip)
		return false
	}

	return info.count >= MaxFailedAttempts
}

func (rl *rateLimiter) recordFail(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	info, exists := rl.attempts[ip]
	if !exists {
		info = &attemptInfo{}
		rl.attempts[ip] = info
	}

	info.count++
	if info.count >= MaxFailedAttempts {
		info.bannedAt = time.Now()
		log.Printf("[SECURITY] IP %s banned for %v after %d failed attempts", ip, BanDuration, info.count)
	}
}

func (rl *rateLimiter) recordSuccess(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	delete(rl.attempts, ip)
}

func (rl *rateLimiter) cleanup() {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	for ip, info := range rl.attempts {
		if !info.bannedAt.IsZero() && time.Since(info.bannedAt) > BanDuration {
			delete(rl.attempts, ip)
		}
	}
}

// === CREDENTIAL LOADING ===
func getCredentials() (string, string) {
	user := os.Getenv("OPENCODE_PROXY_USER")
	pass := os.Getenv("OPENCODE_PROXY_PASS")

	if user == "" || pass == "" {
		fmt.Println("  Error: Credentials not configured.")
		fmt.Println("  Set the environment variables:")
		fmt.Println("    export OPENCODE_PROXY_USER='your_username'")
		fmt.Println("    export OPENCODE_PROXY_PASS='your_password'")
		os.Exit(1)
	}

	return user, pass
}

// findDesktopServer auto-detects the running OpenCode desktop app
func findDesktopServer() *DesktopTarget {
	out, err := exec.Command("bash", "-c",
		`ps -eo pid,command | grep "serve.*--port" | grep "opencode" | grep -v grep`).Output()
	if err != nil || len(out) == 0 {
		return nil
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 {
		return nil
	}

	firstLine := strings.TrimSpace(lines[0])
	fields := strings.Fields(firstLine)
	if len(fields) < 2 {
		return nil
	}
	pid := fields[0]

	portRe := regexp.MustCompile(`--port\s+(\d+)`)
	portMatch := portRe.FindStringSubmatch(firstLine)
	if len(portMatch) < 2 {
		return nil
	}
	port := portMatch[1]

	envOut, err := exec.Command("bash", "-c",
		fmt.Sprintf("ps eww -p %s 2>/dev/null", pid)).Output()
	if err != nil {
		return nil
	}

	passRe := regexp.MustCompile(`OPENCODE_SERVER_PASSWORD=([^\s]+)`)
	passMatch := passRe.FindStringSubmatch(string(envOut))
	if len(passMatch) < 2 {
		return nil
	}
	password := passMatch[1]

	return &DesktopTarget{
		Host:     "127.0.0.1",
		Port:     port,
		User:     "opencode",
		Password: password,
	}
}

func getLocalIP() string {
	out, err := exec.Command("ipconfig", "getifaddr", "en0").Output()
	if err != nil {
		return "127.0.0.1"
	}
	return strings.TrimSpace(string(out))
}

func getTailscaleIP() string {
	// Method 1: Tailscale CLI
	out, err := exec.Command("/Applications/Tailscale.app/Contents/MacOS/Tailscale", "ip", "-4").Output()
	if err == nil {
		ip := strings.TrimSpace(string(out))
		if ip != "" && !strings.Contains(ip, "error") && !strings.Contains(ip, "failed") && !strings.Contains(ip, "Error") {
			return ip
		}
	}

	// Method 2: utun interfaces
	ifaces, err := net.Interfaces()
	if err == nil {
		for _, iface := range ifaces {
			if !strings.HasPrefix(iface.Name, "utun") {
				continue
			}
			addrs, err := iface.Addrs()
			if err != nil {
				continue
			}
			for _, addr := range addrs {
				var ip net.IP
				switch v := addr.(type) {
				case *net.IPNet:
					ip = v.IP
				case *net.IPAddr:
					ip = v.IP
				}
				if ip == nil || ip.To4() == nil {
					continue
				}
				ipv4 := ip.To4()
				if ipv4[0] == 100 && ipv4[1] >= 64 && ipv4[1] <= 127 {
					return ipv4.String()
				}
			}
		}
	}

	// Method 3: ifconfig
	ifconfigOut, err := exec.Command("bash", "-c", `ifconfig | grep "inet 100\." | awk '{print $2}'`).Output()
	if err == nil {
		ip := strings.TrimSpace(string(ifconfigOut))
		if ip != "" {
			parsed := net.ParseIP(ip).To4()
			if parsed != nil && parsed[0] == 100 && parsed[1] >= 64 && parsed[1] <= 127 {
				return ip
			}
		}
	}

	return ""
}

func getClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// isBrowserAutoRequest returns true for requests the browser makes automatically
// that should NOT count toward rate limiting.
func isBrowserAutoRequest(path string) bool {
	if strings.Contains(path, "favicon") || strings.Contains(path, "apple-touch-icon") ||
		strings.Contains(path, "social-share") {
		return true
	}
	if strings.HasSuffix(path, ".webmanifest") || strings.HasSuffix(path, "manifest.json") {
		return true
	}
	if strings.HasPrefix(path, "/assets/") {
		return true
	}
	if strings.Contains(path, "preload") {
		return true
	}
	return false
}

// checkBasicAuth verifies the remote user credentials
func checkBasicAuth(r *http.Request, remoteUser, remotePass string) bool {
	auth := r.Header.Get("Authorization")
	if auth == "" || !strings.HasPrefix(auth, "Basic ") {
		return false
	}

	decoded, err := base64.StdEncoding.DecodeString(auth[6:])
	if err != nil {
		return false
	}

	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return false
	}

	return parts[0] == remoteUser && parts[1] == remotePass
}

// === PROXY STATE ===
// Thread-safe proxy state that allows hot-swapping the backend target.
// The proxy NEVER exits — if OpenCode is not running, it returns 503
// and keeps polling until it comes back.
type proxyState struct {
	mu        sync.RWMutex
	target    *DesktopTarget
	proxy     *httputil.ReverseProxy
	available bool
}

func newProxyState() *proxyState {
	return &proxyState{}
}

func (ps *proxyState) updateTarget(target *DesktopTarget) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	targetURL, _ := url.Parse(fmt.Sprintf("http://%s:%s", target.Host, target.Port))
	rp := httputil.NewSingleHostReverseProxy(targetURL)
	originalDirector := rp.Director
	rp.Director = func(req *http.Request) {
		originalDirector(req)
		auth := base64.StdEncoding.EncodeToString(
			[]byte(fmt.Sprintf("%s:%s", target.User, target.Password)))
		req.Header.Set("Authorization", "Basic "+auth)
		req.Header.Set("Host", fmt.Sprintf("%s:%s", target.Host, target.Port))
		req.Host = fmt.Sprintf("%s:%s", target.Host, target.Port)
	}
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("[ERROR] Proxy error: %v", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, `{"error":"OpenCode desktop not available, will reconnect automatically"}`)
	}

	ps.target = target
	ps.proxy = rp
	ps.available = true
}

func (ps *proxyState) setUnavailable() {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.available = false
}

func (ps *proxyState) serveHTTP(w http.ResponseWriter, r *http.Request) {
	ps.mu.RLock()
	defer ps.mu.RUnlock()

	if !ps.available || ps.proxy == nil {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "10")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"OpenCode desktop not running. Waiting for it to start..."}`)
		return
	}

	ps.proxy.ServeHTTP(w, r)
}

func (ps *proxyState) getTarget() *DesktopTarget {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return ps.target
}

func (ps *proxyState) isAvailable() bool {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return ps.available
}

func main() {
	// Load credentials from environment
	remoteUser, remotePass := getCredentials()

	// Create proxy state (starts with no backend — never exits)
	state := newProxyState()

	// Try to detect desktop server at startup (non-blocking)
	target := findDesktopServer()
	if target != nil {
		state.updateTarget(target)
		log.Printf("[INIT] Desktop detected on port %s", target.Port)
	} else {
		log.Println("[INIT] OpenCode desktop not found, proxy will wait and auto-connect...")
	}

	// Main handler
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clientIP := getClientIP(r)

		// Rate limiting check
		if limiter.isBlocked(clientIP) {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, "Too many failed attempts. Try again later.")
			return
		}

		// CORS preflight
		if r.Method == "OPTIONS" {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, PATCH, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Accept")
			w.Header().Set("Access-Control-Max-Age", "86400")
			w.WriteHeader(http.StatusOK)
			return
		}

		// Check authentication
		if !checkBasicAuth(r, remoteUser, remotePass) {
			if !isBrowserAutoRequest(r.URL.Path) {
				limiter.recordFail(clientIP)
				remaining := MaxFailedAttempts - limiter.attempts[clientIP].count
				if remaining > 0 {
					log.Printf("[AUTH FAIL] %s from %s (%d attempts remaining)", r.URL.Path, clientIP, remaining)
				}
			}
			w.Header().Set("WWW-Authenticate", `Basic realm="OpenCode Remote"`)
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, "Unauthorized")
			return
		}

		// Auth success
		limiter.recordSuccess(clientIP)

		// Security headers
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")

		// Forward to OpenCode (or return 503 if not available)
		state.serveHTTP(w, r)
	})

	// Banner
	localIP := getLocalIP()
	tailscaleIP := getTailscaleIP()

	fmt.Println("")
	fmt.Println("  =============================================")
	fmt.Println("    OpenCode Remote Proxy — Secured")
	fmt.Println("  =============================================")
	fmt.Println("")
	if state.isAvailable() {
		fmt.Printf("  Desktop:    localhost:%s\n", state.getTarget().Port)
	} else {
		fmt.Println("  Desktop:    waiting for OpenCode to start...")
	}
	fmt.Println("")
	fmt.Printf("  Localhost:  http://localhost:%s\n", ProxyPort)
	if tailscaleIP != "" {
		fmt.Printf("  Tailscale:  http://%s:%s\n", tailscaleIP, ProxyPort)
	} else {
		fmt.Println("  Tailscale:  (not detected yet, will auto-bind when available)")
	}
	fmt.Printf("  WiFi (LAN): http://%s:%s\n", localIP, ProxyPort)
	fmt.Println("")
	fmt.Println("  Credentials: loaded from environment variables")
	fmt.Printf("  Rate limit:  %d failed attempts -> %v ban\n", MaxFailedAttempts, BanDuration)
	interfaces := "127.0.0.1"
	if tailscaleIP != "" {
		interfaces += " + " + tailscaleIP
	}
	if localIP != "" && localIP != "127.0.0.1" {
		interfaces += " + " + localIP
	}
	fmt.Printf("  Listening:   %s (not 0.0.0.0)\n", interfaces)
	fmt.Printf("  Poll:        every %v\n", PollInterval)
	fmt.Println("")

	// Build listen addresses
	listenAddrs := []string{"127.0.0.1"}
	if tailscaleIP != "" {
		listenAddrs = append(listenAddrs, tailscaleIP)
	}
	if localIP != "" && localIP != "127.0.0.1" {
		listenAddrs = append(listenAddrs, localIP)
	}

	// Track servers
	var mu sync.Mutex
	var servers []*http.Server
	tailscaleListening := tailscaleIP != ""

	startServer := func(addr string) (*http.Server, error) {
		bindAddr := fmt.Sprintf("%s:%s", addr, ProxyPort)
		// Pre-bind to verify the address is available before starting
		ln, err := net.Listen("tcp", bindAddr)
		if err != nil {
			return nil, fmt.Errorf("bind %s: %w", bindAddr, err)
		}
		srv := &http.Server{
			Handler:      handler,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 0,
			IdleTimeout:  120 * time.Second,
		}
		go func() {
			log.Printf("Proxy listening on %s", bindAddr)
			if err := srv.Serve(ln); err != http.ErrServerClosed {
				log.Printf("Server error on %s: %v", bindAddr, err)
			}
		}()
		return srv, nil
	}

	// Start listeners immediately (even if OpenCode is not running yet)
	for _, addr := range listenAddrs {
		srv, err := startServer(addr)
		if err != nil {
			log.Printf("[WARN] Could not bind %s: %v", addr, err)
			if addr == tailscaleIP {
				tailscaleListening = false
			}
			continue
		}
		servers = append(servers, srv)
	}

	// Graceful shutdown
	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan
		fmt.Println("\n  Stopping proxy...")
		mu.Lock()
		for _, srv := range servers {
			srv.Close()
		}
		mu.Unlock()
		os.Exit(0)
	}()

	// === MAIN LOOP: runs forever, never exits ===
	// Polls for OpenCode desktop every 10s, reconnects on port/password change,
	// auto-binds Tailscale, cleans up rate limiter
	for {
		time.Sleep(PollInterval)
		limiter.cleanup()

		// --- Desktop auto-detection / reconnection ---
		newTarget := findDesktopServer()
		currentTarget := state.getTarget()

		if newTarget != nil {
			if currentTarget == nil ||
				newTarget.Port != currentTarget.Port ||
				newTarget.Password != currentTarget.Password {
				if currentTarget == nil {
					log.Printf("[CONNECTED] OpenCode desktop found on port %s", newTarget.Port)
				} else {
					log.Printf("[RECONNECT] OpenCode changed: port %s -> %s", currentTarget.Port, newTarget.Port)
				}
				state.updateTarget(newTarget)
			}
		} else {
			if state.isAvailable() {
				log.Println("[DISCONNECTED] OpenCode desktop stopped, waiting for restart...")
				state.setUnavailable()
			}
		}

		// --- Tailscale auto-bind ---
		mu.Lock()
		if !tailscaleListening {
			tsIP := getTailscaleIP()
			if tsIP != "" {
				srv, err := startServer(tsIP)
				if err != nil {
					log.Printf("[WARN] Tailscale IP %s detected but bind failed: %v (will retry)", tsIP, err)
				} else {
					log.Printf("[INFO] Tailscale listener started on %s", tsIP)
					servers = append(servers, srv)
					tailscaleListening = true
				}
			}
		}
		mu.Unlock()
	}
}
