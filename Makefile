VERSION ?= 1.0.0
FRP_VERSION ?= 0.71.1
ARCH ?= arm_64

BIN := frpc-qnap-manager
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: help fmt vet build run arm64 x86_64 cross web qpkg clean

help:
	@echo "make build        本机编译（macOS 开发用，产物在 build/）"
	@echo "make run          本机开发模式运行（前端读磁盘，数据目录 devdata/）"
	@echo "make arm64        交叉编译 ARM64 管理器 + 拉取 frpc（默认架构）"
	@echo "make x86_64       交叉编译 x86_64 管理器 + 拉取 frpc"
	@echo "make qpkg         在 Linux + QDK 环境打包（ARCH=$(ARCH)）"
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
	sh scripts/fetch-frp.sh $(FRP_VERSION) $$ga package/$(ARCH)

web:
	rm -rf package/shared/web
	mkdir -p package/shared/web
	cp -R internal/webui/assets/. package/shared/web/

qpkg:
	@$(MAKE) web
	@sh scripts/build-qpkg.sh $(ARCH)

clean:
	rm -rf build dist devdata package/shared/web
	rm -f package/arm_64/$(BIN) package/x86_64/$(BIN)
