-- Первая миграция: портфели и позиции.
--
-- Формат goose: секции Up и Down в одном файле, их отмечают специальные
-- комментарии-аннотации ниже. ВАЖНО: goose ищет слово goose с плюсом
-- перед ним в ЛЮБОЙ строке-комментарии, даже в кавычках посреди текста,
-- и примет его за директиву. В пояснениях эту комбинацию писать нельзя.
-- Flyway откат держит в отдельных U-файлах (и только в платной версии),
-- а goose откатывает бесплатно: `goose down` выполнит секцию Down.
-- Каждая миграция goose по умолчанию выполняется в транзакции — в Postgres
-- DDL транзакционный, так что упавшая миграция не оставит схему наполовину.

-- +goose Up

-- Портфель — именованный набор позиций.
CREATE TABLE portfolios (
    -- identity — стандартный SQL-аналог serial; в Java это @GeneratedValue(strategy = IDENTITY).
    -- bigint в Go маппится в int64.
    id         bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       text        NOT NULL CHECK (length(trim(name)) > 0),
    -- timestamptz хранит момент времени (UTC) — в Go это time.Time,
    -- в Java — Instant/OffsetDateTime. Голый timestamp без зоны не используем.
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Позиция — сколько бумаг с данным тикером лежит в портфеле и по какой средней цене куплены.
CREATE TABLE positions (
    id           bigint         GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- ON DELETE CASCADE: удалили портфель — позиции удалятся вместе с ним
    -- (как orphanRemoval/cascade в JPA, только на уровне БД).
    portfolio_id bigint         NOT NULL REFERENCES portfolios (id) ON DELETE CASCADE,
    -- Тикер в ISS — SECID, например SBER, GAZP. Храним в верхнем регистре.
    ticker       text           NOT NULL CHECK (ticker <> '' AND ticker = upper(ticker)),
    -- Количество штук (не лотов). Акции и облигации — целые; для валюты
    -- с дробным количеством позже можно перейти на numeric.
    quantity     bigint         NOT NULL CHECK (quantity > 0),
    -- Средняя цена покупки за штуку, в рублях. Нужна для расчёта доходности:
    -- (текущая цена - средняя цена) * количество.
    -- numeric — точная десятичная арифметика (как BigDecimal). В Go будет
    -- shopspring/decimal. float/double для денег не используем никогда.
    avg_price    numeric(20, 6) NOT NULL CHECK (avg_price >= 0),
    created_at   timestamptz    NOT NULL DEFAULT now(),
    updated_at   timestamptz    NOT NULL DEFAULT now(),

    -- Один тикер — одна позиция в портфеле; докупка меняет quantity и avg_price.
    -- Уникальный индекс заодно ускоряет поиск позиций по portfolio_id
    -- (portfolio_id — первая колонка индекса), отдельный индекс на FK не нужен.
    UNIQUE (portfolio_id, ticker)
);

-- +goose Down
DROP TABLE positions;
DROP TABLE portfolios;
