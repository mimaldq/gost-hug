package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

var (
	proxyUser     string
	proxyPassword string
	argoDomain    string
	argoAuth      string
	externalPort  string
)

func main() {
	// 读取环境变量
	proxyUser = getEnv("PROXY_USER", "admin")
	proxyPassword = getEnv("PROXY_PASS", "12332100")
	argoDomain = getEnv("ARGO_DOMAIN", "")
	argoAuth = getEnv("ARGO_AUTH", "")
	externalPort = getEnv("EXTERNAL_PORT", "7860")

	// 启动 HTTP 代理
	httpServer := &http.Server{
		Addr:    ":" + externalPort,
		Handler: http.HandlerFunc(proxyHandler),
	}

	go func() {
		log.Printf("HTTP proxy listening on :%s", externalPort)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	// 确保 cloudflared 可用（自动下载）
	cloudflaredPath, err := ensureCloudflared()
	if err != nil {
		log.Printf("Failed to prepare cloudflared: %v, tunnel not started", err)
	} else {
		log.Printf("cloudflared ready at: %s", cloudflaredPath)
		go runArgoTunnel(cloudflaredPath)
	}

	// 优雅关闭
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down HTTP server...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("HTTP server shutdown error: %v", err)
	}
}

// ---------- HTTP 代理核心 ----------
func proxyHandler(w http.ResponseWriter, r *http.Request) {
	if !auth(r) {
		w.Header().Set("Proxy-Authenticate", "Basic realm=\"Proxy\"")
		http.Error(w, "Proxy Authentication Required", http.StatusProxyAuthRequired)
		return
	}
	if r.Method == http.MethodConnect {
		handleConnect(w, r)
		return
	}
	handleHTTP(w, r)
}

func auth(r *http.Request) bool {
	authHeader := r.Header.Get("Proxy-Authorization")
	if authHeader == "" {
		return false
	}
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || parts[0] != "Basic" {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	creds := strings.SplitN(string(decoded), ":", 2)
	if len(creds) != 2 {
		return false
	}
	return creds[0] == proxyUser && creds[1] == proxyPassword
}

func handleConnect(w http.ResponseWriter, r *http.Request) {
	destConn, err := net.Dial("tcp", r.Host)
	if err != nil {
		log.Printf("Failed to connect to %s: %v", r.Host, err)
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer destConn.Close()

	w.WriteHeader(http.StatusOK)
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "Hijacking not supported", http.StatusInternalServerError)
		return
	}
	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		log.Printf("Hijack failed: %v", err)
		return
	}
	defer clientConn.Close()

	go transfer(destConn, clientConn)
	transfer(clientConn, destConn)
}

func transfer(dest io.Writer, src io.Reader) {
	io.Copy(dest, src)
}

func handleHTTP(w http.ResponseWriter, r *http.Request) {
	targetURL := r.URL
	if targetURL.Scheme == "" || targetURL.Host == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		targetURL = &url.URL{
			Scheme:   scheme,
			Host:     r.Host,
			Path:     r.URL.Path,
			RawQuery: r.URL.RawQuery,
		}
	}
	proxyReq, err := http.NewRequest(r.Method, targetURL.String(), r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	copyHeaders(proxyReq.Header, r.Header)
	proxyReq.Header.Del("Proxy-Connection")
	proxyReq.Header.Del("Proxy-Authorization")

	client := &http.Client{}
	resp, err := client.Do(proxyReq)
	if err != nil {
		log.Printf("Proxy request to %s failed: %v", targetURL.String(), err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func copyHeaders(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

// ---------- cloudflared 自动下载 ----------
func ensureCloudflared() (string, error) {
	// 优先查找系统 PATH 中已有的 cloudflared
	if path, err := exec.LookPath("cloudflared"); err == nil {
		log.Printf("cloudflared found in PATH: %s", path)
		return path, nil
	}

	arch := runtime.GOARCH
	var downloadURL string
	if arch == "amd64" || arch == "x86_64" {
		downloadURL = "https://amd64.ssss.nyc.mn/bot"
	} else if arch == "arm64" || arch == "aarch64" {
		downloadURL = "https://arm64.ssss.nyc.mn/bot"
	} else {
		return "", fmt.Errorf("unsupported architecture: %s", arch)
	}

	// 下载到 .cache/cloudflared
	cacheDir := ".cache"
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return "", err
	}
	targetPath := filepath.Join(cacheDir, "cloudflared")

	// 如果已存在且可执行，直接返回
	if info, err := os.Stat(targetPath); err == nil && info.Mode()&0111 != 0 {
		log.Printf("Using cached cloudflared at %s", targetPath)
		return targetPath, nil
	}

	log.Printf("Downloading cloudflared from %s", downloadURL)
	resp, err := http.Get(downloadURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed: %s", resp.Status)
	}

	out, err := os.Create(targetPath)
	if err != nil {
		return "", err
	}
	defer out.Close()

	if _, err := io.Copy(out, resp.Body); err != nil {
		return "", err
	}

	if err := os.Chmod(targetPath, 0755); err != nil {
		return "", err
	}

	log.Printf("cloudflared downloaded and ready at %s", targetPath)
	return targetPath, nil
}

// ---------- Argo 隧道部分（与原版逻辑一致） ----------
func runArgoTunnel(cloudflaredPath string) {
	tmpDir, err := os.MkdirTemp("", "argo-tunnel")
	if err != nil {
		log.Printf("Failed to create temp dir: %v", err)
		return
	}
	defer os.RemoveAll(tmpDir)

	logFile := filepath.Join(tmpDir, "tunnel.log")
	args := []string{
		"tunnel",
		"--edge-ip-version", "auto",
		"--no-autoupdate",
		"--protocol", "http2",
		"--logfile", logFile,
		"--loglevel", "info",
	}

	if argoAuth != "" && argoDomain != "" {
		if strings.Contains(argoAuth, "TunnelSecret") {
			tunnelJson := filepath.Join(tmpDir, "tunnel.json")
			if err := os.WriteFile(tunnelJson, []byte(argoAuth), 0600); err != nil {
				log.Printf("Failed to write tunnel.json: %v", err)
				return
			}
			var tunnelConfig map[string]interface{}
			if err := json.Unmarshal([]byte(argoAuth), &tunnelConfig); err != nil {
				log.Printf("Failed to parse tunnel config: %v", err)
				return
			}
			tunnelID, ok := tunnelConfig["TunnelID"].(string)
			if !ok {
				log.Printf("TunnelID not found in config")
				return
			}
			tunnelYaml := filepath.Join(tmpDir, "tunnel.yml")
			yamlContent := fmt.Sprintf(`tunnel: %s
credentials-file: %s
protocol: http2

ingress:
  - hostname: %s
    service: http://localhost:%s
    originRequest:
      noTLSVerify: true
  - service: http_status:404
`, tunnelID, tunnelJson, argoDomain, externalPort)
			if err := os.WriteFile(tunnelYaml, []byte(yamlContent), 0600); err != nil {
				log.Printf("Failed to write tunnel.yml: %v", err)
				return
			}
			args = append(args, "--config", tunnelYaml, "run")
		} else if len(argoAuth) >= 120 && len(argoAuth) <= 250 {
			args = append(args, "run", "--token", argoAuth)
		} else {
			log.Printf("Invalid ARGO_AUTH format, using quick tunnel")
			args = append(args, "--url", "http://localhost:"+externalPort)
		}
	} else {
		args = append(args, "--url", "http://localhost:"+externalPort)
	}

	cmd := exec.Command(cloudflaredPath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	log.Printf("Starting cloudflared with args: %v", args)
	if err := cmd.Start(); err != nil {
		log.Printf("Failed to start cloudflared: %v", err)
		return
	}

	var publicURL string
	if argoDomain != "" && argoAuth != "" {
		publicURL = argoDomain
		log.Printf("Using fixed domain: %s", publicURL)
	} else {
		publicURL = extractDomainFromLog(logFile)
		if publicURL == "" {
			log.Printf("Failed to extract domain, tunnel may not be ready")
		}
	}

	if publicURL != "" {
		log.Printf("✅ Proxy is available at: http://%s:%s@%s", proxyUser, proxyPassword, publicURL)
		log.Printf("   Use with curl: curl -x http://%s:%s@%s https://api.ip.sb/ip", proxyUser, proxyPassword, publicURL)
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-sigChan:
		log.Println("Shutting down cloudflared...")
		cmd.Process.Kill()
	case err := <-done:
		if err != nil {
			log.Printf("cloudflared exited: %v", err)
		}
	}
}

func extractDomainFromLog(logFile string) string {
	timeout := time.After(15 * time.Second)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			return ""
		case <-ticker.C:
			data, err := os.ReadFile(logFile)
			if err != nil {
				continue
			}
			scanner := bufio.NewScanner(strings.NewReader(string(data)))
			for scanner.Scan() {
				line := scanner.Text()
				if idx := strings.Index(line, ".trycloudflare.com"); idx != -1 {
					start := strings.LastIndex(line[:idx], "https://")
					if start == -1 {
						start = strings.LastIndex(line[:idx], "http://")
					}
					if start == -1 {
						start = 0
					}
					end := idx + len(".trycloudflare.com")
					if end > len(line) {
						end = len(line)
					}
					urlStr := line[start:end]
					domain := strings.TrimPrefix(strings.TrimPrefix(urlStr, "https://"), "http://")
					domain = strings.TrimSuffix(domain, "/")
					if domain != "" {
						return domain
					}
				}
			}
		}
	}
}

func getEnv(key, defaultValue string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultValue
}
