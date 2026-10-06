-- Запросы к портфелям и позициям.
--
-- Каждый запрос начинается с аннотации sqlc: "-- name: <ИмяМетода> <режим>".
-- Режимы: :one (одна строка), :many (слайс), :exec (без результата),
-- :execrows (вернуть число затронутых строк).
-- Параметры: $1, $2 или именованные sqlc.arg(name) — из них получатся
-- поля структуры параметров в Go.

-- name: CreatePortfolio :one
INSERT INTO portfolios (name)
VALUES ($1)
RETURNING id, name, created_at;

-- name: GetPortfolio :one
SELECT id, name, created_at
FROM portfolios
WHERE id = $1;

-- name: ListPortfolios :many
SELECT id, name, created_at
FROM portfolios
ORDER BY id;

-- name: ListPositions :many
SELECT id, portfolio_id, ticker, quantity, avg_price, created_at, updated_at
FROM positions
WHERE portfolio_id = $1
ORDER BY ticker;

-- UpsertPosition создаёт позицию или полностью заменяет количество и цену
-- существующей (семантика HTTP PUT). Учёт отдельных сделок с пересчётом
-- средней цены — отдельная задача на будущее.
--
-- name: UpsertPosition :one
INSERT INTO positions (portfolio_id, ticker, quantity, avg_price)
VALUES (sqlc.arg(portfolio_id), sqlc.arg(ticker), sqlc.arg(quantity), sqlc.arg(avg_price))
ON CONFLICT (portfolio_id, ticker) DO UPDATE
SET quantity   = EXCLUDED.quantity,
    avg_price  = EXCLUDED.avg_price,
    updated_at = now()
RETURNING id, portfolio_id, ticker, quantity, avg_price, created_at, updated_at;

-- name: DeletePosition :execrows
DELETE FROM positions
WHERE portfolio_id = $1 AND ticker = $2;
