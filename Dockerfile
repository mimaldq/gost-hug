FROM alpine:latest

# 安装 bash, ca-certificates, curl (用于下载 bot)
RUN apk add --no-cache bash ca-certificates curl

# 复制 gost 二进制（必须已存在）
COPY gost /usr/local/bin/gost
RUN chmod +x /usr/local/bin/gost

# 复制启动脚本
COPY run.sh /run.sh
RUN chmod +x /run.sh

# 暴露端口
EXPOSE 7860

CMD ["/run.sh"]
