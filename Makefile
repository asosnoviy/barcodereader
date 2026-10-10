# Команды разработки. Go берётся из PATH; если не установлен — см. README.

GO ?= go
BIN_DIR := bin
BIN := $(BIN_DIR)/barcodereader

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
	$(GO) build -trimpath -o $(BIN) ./cmd/barcodereader

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