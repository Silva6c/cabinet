# 内阁 Go 端工程入口（make 不可用时用 scripts\build-exe.bat，二者等价）
#
# 版本注入：Version（internal/server/protocol.go）在构建期写入
#   vc<git提交数>-<HEAD日期>；git 不可用时回退 dev-0。

GO ?= go
VC := $(shell git rev-list HEAD --count 2>/dev/null)
BD := $(shell git log -1 --format=%cd --date=format:%Y%m%d 2>/dev/null)
ifeq ($(VC),)
  VERSION := dev-0
else
  VERSION := vc$(VC)-$(BD)
endif
LDFLAGS := -H windowsgui -X cabinet/internal/server.Version=$(VERSION)

# MinGW 探测：CGO 构建（Fyne/GL）需要 .tools 下的 gcc；缺失时报错指引
MINGW_BIN := .tools/mingw64-extract/mingw64/bin
ifeq ($(wildcard $(MINGW_BIN)/gcc.exe),)
  GCC_MISSING := 1
endif

.PHONY: build test vet lint fmt tidy

## build：托盘版 cabinet.exe（windowsgui 子系统，版本注入）
build:
ifdef GCC_MISSING
	$(error 未找到 $(MINGW_BIN)/gcc.exe：从 winlibs.com 下载 zip 解压到 .tools/mingw64-extract（见 README）)
endif
	PATH="$(CURDIR)/$(MINGW_BIN):$$PATH" CGO_ENABLED=1 \
	$(GO) build -ldflags "$(LDFLAGS)" -o cabinet.exe ./cmd/cabinet

## test：全量单元测试（Fyne 的 CGO 依赖需要 MinGW 在 PATH）
test:
ifdef GCC_MISSING
	$(error 未找到 $(MINGW_BIN)/gcc.exe：从 winlibs.com 下载 zip 解压到 .tools/mingw64-extract（见 README）)
endif
	PATH="$(CURDIR)/$(MINGW_BIN):$$PATH" CGO_ENABLED=1 $(GO) test ./...

## vet：官方静态检查
vet:
	$(GO) vet ./...

## lint：golangci-lint 全仓门禁（.golangci.yml）
lint:
	golangci-lint run

## fmt：格式基线（提交前必过）
fmt:
	gofmt -w cmd internal tools

## tidy：依赖整理
tidy:
	$(GO) mod tidy
