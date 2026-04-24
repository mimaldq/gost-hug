FROM alpine:latest

COPY gost /usr/local/bin/gost
RUN chmod +x /usr/local/bin/gost

# 声明容器内服务监听的端口（仅作文档用途）
EXPOSE 7860

CMD ["/usr/local/bin/gost", "-L", "admin:12332100@:7860"]
