// Package migrations хранит SQL-миграции схемы и применяет их через goose.
//
// Java-параллель: goose — аналог Flyway. Файлы 00001_*.sql соответствуют
// V1__*.sql, а служебная таблица goose_db_version — flyway_schema_history.
// Как и в Spring Boot с Flyway, миграции накатываются при старте приложения.
//
// Главный нюанс — //go:embed: SQL-файлы вкомпилированы прямо в бинарник
// (как ресурсы в src/main/resources внутри jar). Бинарник самодостаточен:
// рядом с ним не нужно класть папку migrations.
//
// Почему этот Go-файл лежит прямо в migrations/: директива go:embed видит
// только файлы в своём каталоге и ниже — подняться через "../" нельзя.
// Поэтому код, который встраивает файлы, должен быть рядом с ними.
package migrations

import (
	"context"
	"embed"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// Комментарий //go:embed ниже — не просто комментарий, а инструкция
// компилятору (пробела после // быть не должно). Он заполнит переменную
// files содержимым всех *.sql из этого каталога при сборке.
//
// embed.FS реализует стандартный интерфейс fs.FS — "файловую систему
// только для чтения". goose принимает именно fs.FS, поэтому ему всё равно,
// читать с диска или из бинарника.
//
//go:embed *.sql
var files embed.FS

// Up применяет все ещё не применённые миграции.
//
// goose работает через стандартный database/sql, а у нас пул pgx.
// stdlib.OpenDBFromPool — адаптер: даёт *sql.DB поверх того же пула,
// без второго набора соединений.
func Up(ctx context.Context, pool *pgxpool.Pool) error {
	db := stdlib.OpenDBFromPool(pool)
	// Закрывает только обёртку *sql.DB; сам пул продолжит работать.
	defer db.Close()

	// Provider — объектный API goose (без глобального состояния, в отличие
	// от старых goose.Up(db, dir)). Глобальное состояние в Go не любят по тем
	// же причинам, что и static-синглтоны в Java: его сложно тестировать.
	provider, err := goose.NewProvider(goose.DialectPostgres, db, files)
	if err != nil {
		return fmt.Errorf("create goose provider: %w", err)
	}

	results, err := provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	// range по слайсу отдаёт пары (индекс, значение); индекс не нужен — "_".
	for _, r := range results {
		slog.Info("migration applied",
			"version", r.Source.Version,
			"file", r.Source.Path,
			"duration", r.Duration,
		)
	}
	if len(results) == 0 {
		slog.Info("database schema is up to date")
	}
	return nil
}
