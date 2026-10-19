# syntax=docker/dockerfile:1
# 多阶段构建：静态二进制 + distroless，非 root 运行。
# 构建：docker build -f deploy/Dockerfile -t emby-go .
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/emby-go ./cmd/metatube

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/emby-go /usr/local/bin/emby-go
# 配置与 SQLite 都在 /data（挂载卷）；无 Redis 时自动回退内存缓存。
VOLUME ["/data"]
EXPOSE 8096
ENTRYPOINT ["/usr/local/bin/emby-go", "-c", "/data/config.yaml"]
