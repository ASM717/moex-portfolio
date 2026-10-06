// Package postgres создаёт пул соединений к PostgreSQL.
//
// Java-параллель: это то, что в Spring Boot делает автоконфигурация
// DataSource + HikariCP. Здесь пул — pgxpool из драйвера pgx.
//
// Почему pgx, а не стандартный database/sql: database/sql — общий
// интерфейс (как JDBC), а pgx — нативный драйвер Postgres с собственным
// API. Он быстрее, лучше знает типы Postgres (numeric, jsonb, массивы)
// и умеет LISTEN/NOTIFY, COPY и т.п. sqlc умеет генерировать код прямо под pgx.
package postgres

import (
	"context"
	"fmt"
	"time"

	// Псевдоним импорта: имя пакета в модуле — decimal, но так оно
	// конфликтовало бы с shopspring/decimal по смыслу; pgxdecimal понятнее.
	pgxdecimal "github.com/jackc/pgx-shopspring-decimal"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool создаёт пул соединений и проверяет, что база доступна.
//
// Возвращает *pgxpool.Pool — указатель, потому что пул — это один общий
// объект с внутренним состоянием (соединения, мьютексы). Копировать его
// нельзя, им делятся. Пул потокобезопасен: один экземпляр на всё приложение,
// как синглтон-бин DataSource.
//
// Закрывать пул (pool.Close()) должен тот, кто его создал, — в нашем случае main.
func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		// Ошибку оборачиваем с контекстом "что мы пытались сделать".
		// В итоге в логе получится цепочка вида
		// "connect to postgres: parse database url: <исходная ошибка>" —
		// читается как stack trace, только короче.
		//
		// Осторожно: не включаем сам databaseURL в текст ошибки — в нём пароль.
		return nil, fmt.Errorf("parse database url: %w", err)
	}

	// Настройки пула — аналог spring.datasource.hikari.*.
	cfg.MaxConns = 10
	cfg.MinConns = 1
	cfg.MaxConnIdleTime = 5 * time.Minute

	// AfterConnect вызывается для каждого нового физического соединения.
	// Регистрируем кодек numeric <-> decimal.Decimal: без него pgx не знает,
	// как читать numeric в decimal напрямую. С кодеком значение передаётся
	// точно, без промежуточного float64 (в Java JDBC сам отдаёт BigDecimal,
	// здесь это подключается явно).
	cfg.AfterConnect = func(_ context.Context, conn *pgx.Conn) error {
		pgxdecimal.Register(conn.TypeMap())
		return nil
	}

	// NewWithConfig не открывает соединения сразу (ленивое подключение),
	// поэтому ошибка здесь — почти всегда ошибка конфигурации.
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	// Ping, чтобы упасть сразу при старте, если база недоступна, а не на
	// первом пользовательском запросе. Таймаут — через дочерний контекст:
	// он отменится сам через 5 секунд или раньше, если отменят родительский ctx.
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel() // освобождаем таймер контекста; забытый cancel — частая утечка, `go vet` об этом предупреждает

	if err := pool.Ping(pingCtx); err != nil {
		pool.Close() // ресурс уже создан — не забываем освободить при ошибке
		return nil, fmt.Errorf("ping: %w", err)
	}

	return pool, nil
}
