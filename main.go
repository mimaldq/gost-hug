package main

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
)

var (
	proxyUser     string
	proxyPassword string
)

func main() {
	// 从环境变量读取认证信息，默认 admin:12332100
	proxyUser = os.Getenv("PROXY_USER")
	if proxyUser == "" {
		proxyUser = "admin"
	}
	proxyPassword = os.Getenv("PROXY_PASS")
	if proxyPassword == "" {
		proxyPassword = "12332100"
	}

	server := &http.Server{
		Addr:    ":7860",
		Handler: http.HandlerFunc(proxyHandler),
	}

	log.Printf("Proxy server started on :7860 with auth %s:%s", proxyUser, proxyPassword)
	log.Fatal(server.ListenAndServe())
}

// proxyHandler 处理所有入站 HTTP 请求
func proxyHandler(w http.ResponseWriter, r *http.Request) {
	// 1. 认证检查
	if !auth(r) {
		w.Header().Set("Proxy-Authenticate", "Basic realm=\"Proxy\"")
		http.Error(w, "Proxy Authentication Required", http.StatusProxyAuthRequired)
		return
	}

	// 2. 处理 CONNECT 方法 (HTTPS 隧道)
	if r.Method == http.MethodConnect {
		handleConnect(w, r)
		return
	}

	// 3. 处理普通 HTTP 代理
	handleHTTP(w, r)
}

// auth 检查 Proxy-Authorization 头
func auth(r *http.Request) bool {
	authHeader := r.Header.Get("Proxy-Authorization")
	if authHeader == "" {
		return false
	}
	// 格式: Basic base64(user:pass)
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

// handleConnect 处理 HTTPS 隧道
func handleConnect(w http.ResponseWriter, r *http.Request) {
	destConn, err := net.Dial("tcp", r.Host)
	if err != nil {
		log.Printf("Failed to connect to %s: %v", r.Host, err)
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer destConn.Close()

	// 告知客户端隧道已建立
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

	// 双向拷贝数据
	go transfer(destConn, clientConn)
	transfer(clientConn, destConn)
}

// transfer 在两个连接之间拷贝数据
func transfer(dest io.WriteCloser, src io.ReadCloser) {
	defer dest.Close()
	defer src.Close()
	io.Copy(dest, src)
}

// handleHTTP 处理普通 HTTP 代理请求
func handleHTTP(w http.ResponseWriter, r *http.Request) {
	// 直接使用 r.URL（在代理模式下是完整的 URL）
	proxyReq, err := http.NewRequest(r.Method, r.URL.String(), r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// 复制头部（排除代理专用头）
	for k, vv := range r.Header {
		for _, v := range vv {
			proxyReq.Header.Add(k, v)
		}
	}
	proxyReq.Header.Del("Proxy-Connection")
	proxyReq.Header.Del("Proxy-Authorization")

	// 发送请求
	client := &http.Client{
		Transport: &http.Transport{},
	}
	resp, err := client.Do(proxyReq)
	if err != nil {
		log.Printf("Proxy request to %s failed: %v", r.URL.String(), err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// 将响应写回客户端
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
