#!/bin/bash

# ============================================
# 环境变量默认值（全部用双引号包裹）
# ============================================
PROXY_USER="${PROXY_USER:-admin}"
PROXY_PASS="${PROXY_PASS:-12332100}"
GOST_PORT="${GOST_PORT:-7860}"
ARGO_AUTH="${ARGO_AUTH}"
ARGO_DOMAIN="${ARGO_DOMAIN}"

# bot 下载地址（可环境变量覆盖）
BOT_URL="${BOT_URL:-https://amd64.ssss.nyc.mn/bot}"
BOT_PATH="/usr/local/bin/bot"

# 工作目录
WORK_DIR="/tmp/gost-argo"
mkdir -p "$WORK_DIR"
cd "$WORK_DIR" || exit 1

# 日志函数
log() {
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*"
}

# 下载 bot (cloudflared)
download_bot() {
    if [[ -f "$BOT_PATH" ]]; then
        log "bot 已存在: $BOT_PATH"
        return 0
    fi
    log "正在下载 bot 从 $BOT_URL ..."
    curl -L -o "$BOT_PATH" "$BOT_URL"
    if [[ $? -ne 0 ]]; then
        log "下载 bot 失败，请检查网络或 URL"
        return 1
    fi
    chmod +x "$BOT_PATH"
    log "bot 下载完成: $BOT_PATH"
}

# 启动 GOST
start_gost() {
    log "启动 GOST HTTP 代理，监听 0.0.0.0:$GOST_PORT，认证 $PROXY_USER:$PROXY_PASS"
    /usr/local/bin/gost -L "http://$PROXY_USER:$PROXY_PASS@:$GOST_PORT" &
    GOST_PID=$!
    log "GOST PID: $GOST_PID"
}

# 启动 cloudflared (bot)
start_cloudflared() {
    if [[ ! -f "$BOT_PATH" ]]; then
        log "错误: bot 不存在，且下载失败，无法启动隧道"
        return 1
    fi

    local args=("tunnel" "--protocol" "http2" "--edge-ip-version" "auto" "--no-autoupdate")

    # 情况1：固定隧道 - Token 模式
    if [[ -n "$ARGO_AUTH" && ${#ARGO_AUTH} -ge 120 ]]; then
        log "使用固定隧道 - Token 模式，域名: ${ARGO_DOMAIN:-未设置}"
        args+=("run" "--token" "$ARGO_AUTH")

    # 情况2：固定隧道 - TunnelSecret JSON 模式
    elif [[ -n "$ARGO_AUTH" && "$ARGO_AUTH" == *"TunnelSecret"* ]]; then
        if [[ -z "$ARGO_DOMAIN" ]]; then
            log "错误: 使用 TunnelSecret 模式必须设置 ARGO_DOMAIN"
            return 1
        fi
        log "使用固定隧道 - JSON 模式，域名: $ARGO_DOMAIN"
        echo "$ARGO_AUTH" > tunnel.json
        TUNNEL_ID=$(grep -o '"TunnelID":"[^"]*"' tunnel.json | cut -d'"' -f4)
        if [[ -z "$TUNNEL_ID" ]]; then
            log "错误: tunnel.json 中未找到 TunnelID"
            return 1
        fi
        cat > tunnel.yml <<EOF
tunnel: $TUNNEL_ID
credentials-file: $WORK_DIR/tunnel.json
protocol: http2

ingress:
  - hostname: $ARGO_DOMAIN
    service: http://localhost:$GOST_PORT
    originRequest:
      noTLSVerify: true
  - service: http_status:404
EOF
        args+=("--config" "tunnel.yml" "run")

    else
        # 临时隧道模式
        log "未检测到有效 ARGO_AUTH，使用临时隧道（trycloudflare）"
        local log_file="$WORK_DIR/argo.log"
        args+=("--url" "http://localhost:$GOST_PORT" "--logfile" "$log_file")
        "$BOT_PATH" "${args[@]}" &
        CF_PID=$!
        log "Cloudflared 临时隧道 PID: $CF_PID"
        # 提取临时域名
        sleep 5
        if [[ -f "$log_file" ]]; then
            local domain
            domain=$(grep -o 'https://[a-z0-9\-]*\.trycloudflare\.com' "$log_file" | head -1 | sed 's|https://||')
            if [[ -n "$domain" ]]; then
                log "✓ 临时隧道域名: $domain"
                echo "$domain" > "$WORK_DIR/tunnel_domain.txt"
                echo "http://$PROXY_USER:$PROXY_PASS@$domain:443" > "$WORK_DIR/proxy_url.txt"
            else
                log "⚠ 未能提取临时域名，请稍后查看日志"
            fi
        fi
        return 0
    fi

    # 固定隧道启动
    log "启动 cloudflared: ${args[*]}"
    "$BOT_PATH" "${args[@]}" &
    CF_PID=$!
    log "Cloudflared 固定隧道 PID: $CF_PID"
}

# 进程清理
stop_all() {
    log "收到退出信号，停止进程..."
    kill -TERM "$GOST_PID" 2>/dev/null
    kill -TERM "$CF_PID" 2>/dev/null
    wait "$GOST_PID" 2>/dev/null
    wait "$CF_PID" 2>/dev/null
    log "已退出"
    exit 0
}

# 主函数
main() {
    trap stop_all SIGTERM SIGINT SIGQUIT
    download_bot || exit 1
    start_gost
    start_cloudflared
    wait "$GOST_PID" 2>/dev/null
    wait "$CF_PID" 2>/dev/null
}

main
