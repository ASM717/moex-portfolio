# syntax=docker/dockerfile:1

# Многоэтапная сборка (multi-stage build).
#
# Этап 1 собирает бинарник в полноценном образе с Go (~900 МБ).
# Этап 2 копирует только готовый бинарник в минимальный образ (~2 МБ + бинарник).
#
# Java-параллель: как собрать jar в образе maven:3-jdk, а запускать на
# eclipse-temurin:jre. Только у Go рантайм не нужен вовсе: бинарник
# статический и содержит в себе всё, включая сборщик мусора и планировщик горутин.

# ---------- Этап 1: сборка ----------
# Версия Go в образе должна быть не ниже директивы go в go.mod (1.26.5).
FROM golang:1.26 AS build

WORKDIR /src

# Сначала только go.mod и go.sum, потом скачивание зависимостей. Docker
# кэширует слои: пока эти два файла не менялись, зависимости не качаются
# заново при каждой правке кода (как отдельный слой с pom.xml + mvn dependency:go-offline).
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

# CGO_ENABLED=0 — чистый Go без C-библиотек: бинарник полностью статический
#   и запускается в образе без libc.
# -trimpath — убрать локальные пути сборки из бинарника (воспроизводимость).
# -ldflags="-s -w" — выбросить таблицу символов и отладочную информацию
#   (бинарник меньше на ~30%; стектрейсы паник при этом сохраняются).
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# ---------- Этап 2: запуск ----------
# distroless/static — минимальный образ от Google: нет shell, пакетного
# менеджера и прочего лишнего (меньше поверхность атаки). Внутри есть то,
# что нужно нашему бинарнику:
#   - корневые сертификаты CA — без них не заработает HTTPS к iss.moex.com;
#   - база часовых поясов;
#   - пользователь nonroot (тег :nonroot) — процесс работает не от root.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/server /server

EXPOSE 8080
USER nonroot:nonroot

# Exec-форма (JSON-массив), а не shell-форма "ENTRYPOINT /server":
# процесс становится PID 1 и сам получает SIGTERM от `docker stop`,
# поэтому срабатывает graceful shutdown из main.go. Shell здесь и нет.
ENTRYPOINT ["/server"]
