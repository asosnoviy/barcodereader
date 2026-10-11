# Команды разработки. Go берётся из PATH; если не установлен — см. README.

GO ?= go
BIN_DIR := bin
BIN := $(BIN_DIR)/barcodereader

# Сведения о сборке попадают в журнал через -ldflags. Без них в логе будет
# "dev", и по журналу нельзя понять, какой файл запущен: у службы это
# Program Files, у разработчика — bin/.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
REVISION ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
PKG := barcodereader/internal/buildinfo
LDFLAGS := -s -w \
	-X '$(PKG).Version=$(VERSION)' \
	-X '$(PKG).Revision=$(REVISION)' \
	-X '$(PKG).BuildTime=$(BUILD_TIME)'

.PHONY: help tidy build test test-race vet fmt run gendata docker-build clean

help: ## Показать список команд
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

tidy: ## Привести go.mod/go.sum в порядок
	$(GO) mod tidy

fmt: ## Отформатировать код
	$(GO) fmt ./...

vet: ## Статический анализ
	$(GO) vet ./...

build: ## Собрать бинарник в bin/
	@mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN) ./cmd/barcodereader

test: ## Прогнать тесты
	$(GO) test ./...

test-race: ## Прогнать тесты с детектором гонок
	$(GO) test -race ./...

run: ## Запустить сервис локально
	$(GO) run ./cmd/barcodereader

gendata: ## Пересоздать PDF-фикстуры в testdata/
	$(GO) run ./cmd/gendata -out testdata

docker-build: ## Собрать Docker-образ
	docker build -t barcodereader:dev .

clean: ## Удалить артефакты сборки
	@rm -rf $(BIN_DIR)