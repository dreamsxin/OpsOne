# OpsOne 镜像：前端构建 → 后端构建 → 运行镜像（单进程同时提供 API 与前端）
#
# 构建：docker build -t opsone:latest --build-arg VERSION=$(git describe --tags --always) .
# 运行：见 docker-compose.yml

FROM node:22-alpine AS web
WORKDIR /src/web
# 依赖源可换：国内直连 registry.npmjs.org 常常超时
ARG NPM_REGISTRY=https://registry.npmjs.org
RUN npm config set registry "$NPM_REGISTRY"
# 先只拷依赖清单，让 pnpm install 能命中缓存层。
# --ignore-scripts：依赖里只有 @parcel/watcher 这类可选原生模块要跑构建脚本（dev 用的文件监听），
# 生产构建不需要；pnpm 10 在「有被忽略的构建脚本」时会直接报 ERR_PNPM_IGNORED_BUILDS。
COPY web/package.json web/pnpm-lock.yaml ./
RUN corepack enable && pnpm install --frozen-lockfile --ignore-scripts
COPY web/ ./
RUN pnpm build

FROM golang:1.26-alpine AS api
WORKDIR /src/server
# 同理：直连 proxy.golang.org 不通时可以 --build-arg GOPROXY=https://goproxy.cn,direct
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=$GOPROXY
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
ARG VERSION=dev
# CGO 关掉：SQLite 用的是纯 Go 实现（glebarez/sqlite），不需要 libc 工具链
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/ops ./cmd/server

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata wget \
    && adduser -D -u 10001 opsone
WORKDIR /app
COPY --from=api /out/ops /usr/local/bin/ops
COPY --from=web /src/web/dist /app/web

# 数据全在 /app/data 里：数据库、录像、备份。挂卷的时候只需要挂这一个目录。
ENV OPS_ENV=prod \
    OPS_ADDR=:8080 \
    OPS_WEB_DIR=/app/web \
    OPS_DSN=/app/data/ops.db \
    OPS_RECORD_DIR=/app/data/recordings \
    OPS_BACKUP_DIR=/app/data/backups \
    TZ=Asia/Shanghai
RUN mkdir -p /app/data/recordings /app/data/backups && chown -R opsone:opsone /app/data
VOLUME ["/app/data"]

USER opsone
EXPOSE 8080
# 隧道端口区间（OPS_FORWARD_PORT_MIN/MAX）要用的话需要自己在 run/compose 里映射
EXPOSE 30000-30099

# 探活用 /readyz：它会真查一次数据库，比 /healthz 更能说明「能干活了」
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/readyz >/dev/null || exit 1

# SIGTERM 是 docker stop 的默认信号，进程会走优雅退出
STOPSIGNAL SIGTERM
ENTRYPOINT ["ops"]
CMD ["serve"]
