VERSION ?= 1.0.0
FRP_VERSION ?= 0.71.1
ARCH ?= arm_64

BIN := frpc-qnap-manager
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: help fmt vet build run arm64 x86_64 cross web qpkg check setup clean

help:
	@echo "make build        本机编译（macOS 开发用，产物在 build/）"
	@echo "make run          本机开发模式运行（前端读磁盘，数据目录 devdata/）"
	@echo "make arm64        交叉编译 ARM64 管理器 + 拉取 frpc（默认架构）"
	@echo "make x86_64       交叉编译 x86_64 管理器 + 拉取 frpc"
	@echo "make check        只做打包源预检（不调 qbuild，Windows/WSL 都能跑）"
	@echo "make qpkg         在 Linux + QDK 环境打包（ARCH=$(ARCH)）"
	@echo "make setup        WSL/Linux 上一键装好依赖 + Go + QDK"
	@echo "make clean        清理构建产物"

fmt:
	gofmt -l -w .

vet:
	go vet ./...

build:
	@mkdir -p build
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o build/$(BIN) ./cmd/frpc-qnap-manager

run:
	go run ./cmd/frpc-qnap-manager -dev -data devdata -listen 127.0.0.1:7911

arm64:
	@$(MAKE) cross ARCH=arm_64

x86_64:
	@$(MAKE) cross ARCH=x86_64

cross:
	@mkdir -p package/$(ARCH)
	@case "$(ARCH)" in \
		arm_64) ga=arm64 ;; \
		x86_64) ga=amd64 ;; \
		*) echo "ARCH 只支持 arm_64 / x86_64" >&2; exit 1 ;; \
	esac; \
	echo "==> 交叉编译 $$ga"; \
	GOOS=linux GOARCH=$$ga CGO_ENABLED=0 go build -trimpath \
		-ldflags "$(LDFLAGS)" -o package/$(ARCH)/$(BIN) ./cmd/frpc-qnap-manager && \
	if [ -x package/$(ARCH)/frpc ]; then \
		echo "==> package/$(ARCH)/frpc 已存在，跳过下载"; \
	else \
		sh scripts/fetch-frp.sh $(FRP_VERSION) $$ga package/$(ARCH); \
	fi

web:
	rm -rf package/shared/web
	mkdir -p package/shared/web
	cp -R internal/webui/assets/. package/shared/web/

qpkg:
	@sh scripts/build-qpkg.sh $(ARCH)

# 打包源预检：不依赖 qbuild，Windows 的 Git Bash 里也能跑（会提示缺少 rsync/qbuild）
check:
	@sh scripts/preflight-qpkg.sh $(ARCH) || [ $$? -eq 2 ]

setup:
	@sh scripts/wsl-setup.sh

clean:
	rm -rf build dist devdata package/shared/web
	rm -f package/arm_64/$(BIN) package/x86_64/$(BIN)
