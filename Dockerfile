FROM alpine:latest

# 安装 bash 和 ca-certificates（临时隧道需要 HTTPS 连接）
RUN apk add --no-cache bash ca-certificates

# 复制二进制文件
COPY gost /usr/local/bin/gost
COPY bot /usr/local/bin/bot

# 赋予执行权限
RUN chmod +x /usr/local/bin/gost /usr/local/bin/bot

# 复制启动脚本
COPY run.sh /run.sh
RUN chmod +x /run.sh

# 暴露端口（gost 监听 7860）
EXPOSE 7860

CMD ["/run.sh"]
