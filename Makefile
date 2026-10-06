# Makefile — короткие команды для частых действий с проектом.
#
# Java-параллель: в Maven/Gradle жизненный цикл встроен (mvn test, gradle bootRun).
# У Go-инструментов команды уже короткие (go test ./...), но вокруг них много
# обвязки — docker compose, линтеры, генерация, миграции. Makefile собирает это
# в одном месте и служит "живой документацией": `make` покажет, что можно сделать.
#
# Синтаксис: "цель: зависимости", ниже команды, каждая строка — с ТАБОМ
# (не пробелами — это классическая ловушка make). Комментарий "## ..." после
# цели попадает в справку `make help`.
#
# Совместимо с GNU Make 3.81 (стоит в macOS по умолчанию).

# Цель по умолчанию — то, что выполнится при простом `make`.
.DEFAULT_GOAL := help

# Пути и переменные. ":=" вычисляет значение один раз при чтении файла.
BIN        := bin/server
COVERAGE   := coverage.out
MIGRATIONS := migrations

# Версия goose для CLI берётся из go.mod, чтобы не разойтись с библиотекой,
# которой сервис накатывает миграции. CLI не подключён как tool-зависимость:
# он тянет драйверы всех СУБД, которые проекту не нужны.
GOOSE_VERSION := $(shell go list -m -f '{{.Version}}' github.com/pressly/goose/v3)
GOOSE         := go run github.com/pressly/goose/v3/cmd/goose@$(GOOSE_VERSION)

# .PHONY — эти цели не файлы. Без этого make решит, что цель "test" уже
# выполнена, если в каталоге окажется файл с именем test.
.PHONY: help run up db down reset-db logs ps psql \
        build docker-build test test-race cover \
        fmt fmt-check vet lint tidy generate generate-check check \
        migration clean

help: ## Показать список команд
	@printf "Использование: make <цель>\n\n"
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*## "}; {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'

# ---------- Запуск и окружение ----------

run: ## Запустить сервис с хоста (нужна база: make db)
	go run ./cmd/server

up: ## Поднять всё в Docker: PostgreSQL + сервис (с пересборкой образа)
	docker compose up -d --build

db: ## Поднять только PostgreSQL (для разработки с make run)
	docker compose up -d postgres

down: ## Остановить контейнеры (данные сохранятся)
	docker compose down

reset-db: ## Удалить контейнеры и базу вместе с данными
	docker compose down -v

logs: ## Логи сервиса в Docker (Ctrl+C — выйти)
	docker compose logs -f app

ps: ## Статус контейнеров
	docker compose ps

psql: ## Консоль PostgreSQL
	docker compose exec postgres psql -U moex -d moex

# ---------- Сборка ----------

# Те же флаги, что в Dockerfile: статический бинарник без путей сборки
# и отладочной информации.
build: ## Собрать бинарник в bin/server
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(BIN) ./cmd/server

docker-build: ## Собрать Docker-образ сервиса
	docker compose build app

# ---------- Тесты ----------

test: ## Запустить тесты
	go test ./...

# -race — детектор гонок данных: инструментирует доступ к памяти и ловит
# одновременную запись/чтение из разных горутин. Медленнее, поэтому отдельно.
test-race: ## Тесты с детектором гонок
	go test -race ./...

# Профиль покрытия и отчёт по функциям; HTML-отчёт: go tool cover -html=coverage.out
cover: ## Тесты с отчётом о покрытии
	go test -coverprofile=$(COVERAGE) ./...
	go tool cover -func=$(COVERAGE) | tail -1

# ---------- Качество кода ----------

# gofmt — единый формат кода для всех Go-проектов, споров о стиле нет.
# Аналог spotless/google-java-format, но встроенный и без настроек.
fmt: ## Отформатировать код (gofmt)
	gofmt -w .

# Проверка без изменений (для CI): выводит неотформатированные файлы и падает.
fmt-check: ## Проверить форматирование
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "Не отформатировано (запустите make fmt):"; echo "$$out"; exit 1; fi

vet: ## Встроенный анализатор go vet
	go vet ./...

# staticcheck подключён как tool-зависимость в go.mod — версия зафиксирована.
lint: vet ## go vet + staticcheck
	go tool staticcheck ./...

tidy: ## Привести go.mod и go.sum в порядок
	go mod tidy

# ---------- Генерация кода ----------

generate: ## Сгенерировать код по SQL-запросам (sqlc)
	go generate ./...

# Проверяет, что сгенерированный код соответствует запросам: копирует
# internal/db во временный каталог, перегенерирует и сравнивает. Git не
# участвует, поэтому незакоммиченные, но синхронные изменения проходят.
# Если код был устаревшим, он уже перегенерирован — остаётся закоммитить.
# $$ — экранирование: make превращает $$ в $, который видит shell.
generate-check: ## Проверить, что сгенерированный код актуален
	@tmp="$$(mktemp -d)"; cp -R internal/db/. "$$tmp"; \
	go generate ./... || { rm -rf "$$tmp"; exit 1; }; \
	if ! diff -rq "$$tmp" internal/db >/dev/null; then \
		echo "Сгенерированный код был устаревшим и перегенерирован — проверьте и закоммитьте:"; \
		diff -rq "$$tmp" internal/db; rm -rf "$$tmp"; exit 1; \
	fi; rm -rf "$$tmp"

# ---------- Сводная проверка ----------

# Зависимости выполняются по порядку слева направо — как набор шагов в CI.
check: fmt-check lint test-race generate-check ## Все проверки перед коммитом
	@echo "Все проверки пройдены"

# ---------- Миграции ----------

# Пример: make migration name=add_trades
migration: ## Создать миграцию: make migration name=<название>
	@if [ -z "$(name)" ]; then echo "Укажите имя: make migration name=add_something"; exit 1; fi
	$(GOOSE) -dir $(MIGRATIONS) -s create $(name) sql

# ---------- Уборка ----------

clean: ## Удалить собранные файлы
	rm -rf bin $(COVERAGE)
