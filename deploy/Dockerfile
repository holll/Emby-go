# syntax=docker/dockerfile:1
# 多阶段构建：静态二进制 + Alpine 运行层。
# 运行层安装 ffmpeg（提供 ffprobe）——媒体信息探测依赖它；镜像因此比 distroless 大，
# 但开箱即用。若确定不用探测，可把运行层换回 distroless（详见 README「Docker」）。
# 构建：docker build -f deploy/Dockerfile -t emby-go .
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/emby-go ./cmd/metatube

FROM alpine:3.21
RUN apk add --no-cache ffmpeg ca-certificates tzdata \
 && addgroup -S app && adduser -S -G app app \
 && mkdir -p /data && chown app:app /data
COPY --from=build /out/emby-go /usr/local/bin/emby-go
USER app
# 配置与 SQLite 都在 /data（挂载卷）；无 Redis 时自动回退内存缓存。
VOLUME ["/data"]
EXPOSE 8096
ENTRYPOINT ["/usr/local/bin/emby-go", "-c", "/data/config.yaml"]
