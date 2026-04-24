FROM golang:1.21-alpine AS builder
WORKDIR /app
COPY main.go .
RUN go build -o proxy main.go

FROM alpine:latest
WORKDIR /root/
COPY --from=builder /app/proxy .
EXPOSE 7860
CMD ["./proxy"]
