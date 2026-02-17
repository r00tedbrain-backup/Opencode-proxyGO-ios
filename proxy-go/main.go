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
	MaxFailedAttempts = 5
	BanDuration       = 15 * time.Minute
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

	// Check if ban has expired
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

// Clean up expired bans periodically
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
	// Method 1: Try the Tailscale CLI
	out, err := exec.Command("/Applications/Tailscale.app/Contents/MacOS/Tailscale", "ip", "-4").Output()
	if err == nil {
		ip := strings.TrimSpace(string(out))
		if ip != "" && !strings.Contains(ip, "error") && !strings.Contains(ip, "failed") && !strings.Contains(ip, "Error") {
			return ip
		}
	}

	// Method 2: Read from the utun interface directly (Tailscale uses 100.x.y.z range)
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
				// Tailscale uses CGNAT range 100.64.0.0/10 (100.64.x.x - 100.127.x.x)
				ipv4 := ip.To4()
				if ipv4[0] == 100 && ipv4[1] >= 64 && ipv4[1] <= 127 {
					return ipv4.String()
				}
			}
		}
	}

	// Method 3: Parse ifconfig output as last resort
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

func main() {
	// Load credentials from environment
	remoteUser, remotePass := getCredentials()

	// Detect desktop server
	target := findDesktopServer()
	if target == nil {
		fmt.Println("  Error: OpenCode desktop app not found running.")
		fmt.Println("  Open the OpenCode application on your Mac and try again.")
		os.Exit(1)
	}

	targetURL, _ := url.Parse(fmt.Sprintf("http://%s:%s", target.Host, target.Port))

	// Create reverse proxy
	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		auth := base64.StdEncoding.EncodeToString(
			[]byte(fmt.Sprintf("%s:%s", target.User, target.Password)))
		req.Header.Set("Authorization", "Basic "+auth)
		req.Header.Set("Host", fmt.Sprintf("%s:%s", target.Host, target.Port))
		req.Host = fmt.Sprintf("%s:%s", target.Host, target.Port)
	}

	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("[ERROR] Proxy error: %v", err)
		newTarget := findDesktopServer()
		if newTarget != nil && (newTarget.Port != target.Port || newTarget.Password != target.Password) {
			log.Printf("[INFO] Desktop server changed to port %s, updating...", newTarget.Port)
			target = newTarget
			newURL, _ := url.Parse(fmt.Sprintf("http://%s:%s", newTarget.Host, newTarget.Port))
			proxy = httputil.NewSingleHostReverseProxy(newURL)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, `{"error":"OpenCode desktop not available"}`)
	}

	// Main handler
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clientIP := getClientIP(r)

		// Rate limiting check
		if limiter.isBlocked(clientIP) {
			log.Printf("[BLOCKED] %s %s from %s (rate limited)", r.Method, r.URL.Path, clientIP)
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
			limiter.recordFail(clientIP)
			remaining := MaxFailedAttempts - limiter.attempts[clientIP].count
			if remaining > 0 {
				log.Printf("[AUTH FAIL] %s from %s (%d attempts remaining)", r.URL.Path, clientIP, remaining)
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

		proxy.ServeHTTP(w, r)
	})

	// Banner
	localIP := getLocalIP()
	tailscaleIP := getTailscaleIP()

	fmt.Println("")
	fmt.Println("  =============================================")
	fmt.Println("    OpenCode Remote Proxy — Secured")
	fmt.Println("  =============================================")
	fmt.Println("")
	fmt.Printf("  Desktop:    localhost:%s\n", target.Port)
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
	fmt.Println("")

	// Build list of specific interfaces to bind to
	listenAddrs := []string{"127.0.0.1"}
	if tailscaleIP != "" {
		listenAddrs = append(listenAddrs, tailscaleIP)
	}
	if localIP != "" && localIP != "127.0.0.1" {
		listenAddrs = append(listenAddrs, localIP)
	}

	// Track active servers and Tailscale listener state
	var mu sync.Mutex
	var servers []*http.Server
	tailscaleListening := tailscaleIP != ""

	// Helper to create and start a server on an address
	startServer := func(addr string) *http.Server {
		bindAddr := fmt.Sprintf("%s:%s", addr, ProxyPort)
		srv := &http.Server{
			Addr:         bindAddr,
			Handler:      handler,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 0, // No timeout for SSE streams
			IdleTimeout:  120 * time.Second,
		}
		go func() {
			log.Printf("Proxy listening on %s", srv.Addr)
			if err := srv.ListenAndServe(); err != http.ErrServerClosed {
				log.Printf("Server error on %s: %v", srv.Addr, err)
			}
		}()
		return srv
	}

	// Start initial servers
	for _, addr := range listenAddrs {
		srv := startServer(addr)
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

	// Periodic: cleanup rate limiter, re-detect desktop, auto-bind Tailscale
	for {
		time.Sleep(30 * time.Second)
		limiter.cleanup()

		// Re-detect desktop server (handles OpenCode restarts / port changes)
		newTarget := findDesktopServer()
		if newTarget != nil {
			if newTarget.Port != target.Port || newTarget.Password != target.Password {
				log.Printf("[INFO] Desktop re-detected on port %s", newTarget.Port)
				target = newTarget
				newURL, _ := url.Parse(fmt.Sprintf("http://%s:%s", newTarget.Host, newTarget.Port))
				proxy = httputil.NewSingleHostReverseProxy(newURL)
				proxy.Director = func(req *http.Request) {
					req.URL.Scheme = newURL.Scheme
					req.URL.Host = newURL.Host
					auth := base64.StdEncoding.EncodeToString(
						[]byte(fmt.Sprintf("%s:%s", target.User, target.Password)))
					req.Header.Set("Authorization", "Basic "+auth)
					req.Host = newURL.Host
				}
			}
		} else {
			log.Println("[WARN] Desktop not detected, waiting...")
		}

		// Auto-bind Tailscale if not yet listening
		mu.Lock()
		if !tailscaleListening {
			tsIP := getTailscaleIP()
			if tsIP != "" {
				log.Printf("[INFO] Tailscale detected (%s), starting listener...", tsIP)
				srv := startServer(tsIP)
				servers = append(servers, srv)
				tailscaleListening = true
			}
		}
		mu.Unlock()
	}
}
