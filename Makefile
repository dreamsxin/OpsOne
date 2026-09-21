# OpsOne 构建与打包
#
#   make build          本机二进制（server/ops）
#   make dist           Linux amd64 二进制 + 前端产物，打成 dist/opsone-<版本>.tar.gz
#   make docker         构建容器镜像
#   make check          gofmt + go vet + go test + 前端类型检查
#
# 版本号来自 git，注入到二进制里（ops version 可以看到）

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
OUT     := dist

.PHONY: help build web dist docker check fmt vet test clean run

help:
	@echo "VERSION=$(VERSION)"
	@echo "targets: build web dist docker check fmt vet test run clean"

build:
	cd server && go build -trimpath -ldflags "$(LDFLAGS)" -o ops ./cmd/server

web:
	cd web && pnpm install --frozen-lockfile && pnpm build

# 交付包：Linux 二进制 + 前端 dist + 部署样例，解开就能按 docs/DEPLOY.md 装
dist: web
	rm -rf $(OUT)/opsone && mkdir -p $(OUT)/opsone
	cd server && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o ../$(OUT)/opsone/ops ./cmd/server
	cp -r web/dist $(OUT)/opsone/web
	cp -r deploy $(OUT)/opsone/deploy
	cp docs/DEPLOY.md $(OUT)/opsone/
	cd $(OUT) && tar czf opsone-$(VERSION)-linux-amd64.tar.gz opsone
	@echo "打好了: $(OUT)/opsone-$(VERSION)-linux-amd64.tar.gz"

docker:
	docker build -t opsone:$(VERSION) -t opsone:latest --build-arg VERSION=$(VERSION) .

check: fmt vet test
	cd web && pnpm build

fmt:
	@out=$$(cd server && gofmt -l .); \
	if [ -n "$$out" ]; then echo "以下文件未格式化（跑 gofmt -w）:"; echo "$$out"; exit 1; fi

vet:
	cd server && go vet ./...

test:
	cd server && go test ./...

run:
	cd server && go run ./cmd/server serve

clean:
	rm -rf $(OUT) server/ops web/dist
