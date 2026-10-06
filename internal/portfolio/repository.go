package portfolio

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ASM717/moex-portfolio/internal/db"
)

// pgForeignKeyViolation — код ошибки Postgres (SQLSTATE) при нарушении
// внешнего ключа. Полный список: https://www.postgresql.org/docs/current/errcodes-appendix.html
const pgForeignKeyViolation = "23503"

// Repository — доступ к портфелям в PostgreSQL.
//
// Тонкая обёртка над сгенерированным sqlc-кодом. Её задачи:
//   - переводить db-типы в доменные (и обратно);
//   - переводить ошибки драйвера в доменные (pgx.ErrNoRows → ErrNotFound),
//     чтобы слои выше ничего не знали о pgx.
//
// Java-параллель: класс-реализация репозитория (@Repository), где Spring
// делает то же самое через PersistenceExceptionTranslation.
type Repository struct {
	q *db.Queries
}

// NewRepository — конструктор. В Go конструкторов в языке нет, это просто
// функция с конвенциональным именем New<Тип>. Возвращаем указатель:
// репозиторий — один долгоживущий объект, им делятся, а не копируют.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{q: db.New(pool)}
}

// CreatePortfolio сохраняет новый портфель.
func (r *Repository) CreatePortfolio(ctx context.Context, name string) (Portfolio, error) {
	row, err := r.q.CreatePortfolio(ctx, name)
	if err != nil {
		return Portfolio{}, fmt.Errorf("insert portfolio: %w", err)
	}
	return toPortfolio(row), nil
}

// GetPortfolio возвращает портфель по id или ошибку, оборачивающую ErrNotFound.
//
// В Go при ошибке принято возвращать "нулевое значение" (Portfolio{}),
// а не nil — для структуры-значения nil невозможен. Вызывающий код обязан
// сначала проверить err и не смотреть на первое значение, если err != nil.
func (r *Repository) GetPortfolio(ctx context.Context, id int64) (Portfolio, error) {
	row, err := r.q.GetPortfolio(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Portfolio{}, fmt.Errorf("portfolio %d: %w", id, ErrNotFound)
		}
		return Portfolio{}, fmt.Errorf("select portfolio %d: %w", id, err)
	}
	return toPortfolio(row), nil
}

// ListPortfolios возвращает все портфели, отсортированные по id.
func (r *Repository) ListPortfolios(ctx context.Context) ([]Portfolio, error) {
	rows, err := r.q.ListPortfolios(ctx)
	if err != nil {
		return nil, fmt.Errorf("select portfolios: %w", err)
	}
	// make([]T, 0, n) — слайс длиной 0 и ёмкостью n: память выделяется
	// один раз, append не будет перевыделять. Аналог new ArrayList<>(n).
	out := make([]Portfolio, 0, len(rows))
	for _, row := range rows {
		out = append(out, toPortfolio(row))
	}
	return out, nil
}

// ListPositions возвращает позиции портфеля, отсортированные по тикеру.
// Существование портфеля не проверяет: для несуществующего вернёт пустой список.
func (r *Repository) ListPositions(ctx context.Context, portfolioID int64) ([]Position, error) {
	rows, err := r.q.ListPositions(ctx, portfolioID)
	if err != nil {
		return nil, fmt.Errorf("select positions of portfolio %d: %w", portfolioID, err)
	}
	out := make([]Position, 0, len(rows))
	for _, row := range rows {
		out = append(out, toPosition(row))
	}
	return out, nil
}

// UpsertPosition создаёт позицию или заменяет количество и цену существующей.
// Если портфеля нет, возвращает ошибку, оборачивающую ErrNotFound.
func (r *Repository) UpsertPosition(ctx context.Context, portfolioID int64, p Position) (Position, error) {
	row, err := r.q.UpsertPosition(ctx, db.UpsertPositionParams{
		PortfolioID: portfolioID,
		Ticker:      p.Ticker,
		Quantity:    p.Quantity,
		AvgPrice:    p.AvgPrice,
	})
	if err != nil {
		// Отдельной проверки "есть ли портфель" перед вставкой не делаем:
		// между проверкой и вставкой портфель могут удалить (race condition).
		// Надёжнее положиться на внешний ключ и разобрать ошибку БД.
		//
		// errors.As ищет в цепочке ошибку нужного ТИПА и кладёт её в pgErr.
		// Передаём указатель на переменную (&pgErr), чтобы As мог её заполнить.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgForeignKeyViolation {
			return Position{}, fmt.Errorf("portfolio %d: %w", portfolioID, ErrNotFound)
		}
		return Position{}, fmt.Errorf("upsert position %s in portfolio %d: %w", p.Ticker, portfolioID, err)
	}
	return toPosition(row), nil
}

// DeletePosition удаляет позицию. Если её нет, возвращает ошибку,
// оборачивающую ErrNotFound.
func (r *Repository) DeletePosition(ctx context.Context, portfolioID int64, ticker string) error {
	n, err := r.q.DeletePosition(ctx, db.DeletePositionParams{
		PortfolioID: portfolioID,
		Ticker:      ticker,
	})
	if err != nil {
		return fmt.Errorf("delete position %s in portfolio %d: %w", ticker, portfolioID, err)
	}
	if n == 0 {
		return fmt.Errorf("position %s in portfolio %d: %w", ticker, portfolioID, ErrNotFound)
	}
	return nil
}

// Функции-мапперы db → домен. Простые функции вместо MapStruct:
// пара строк кода, зато всё явно и проверяется компилятором.

func toPortfolio(row db.Portfolio) Portfolio {
	return Portfolio{
		ID:        row.ID,
		Name:      row.Name,
		CreatedAt: row.CreatedAt,
	}
}

func toPosition(row db.Position) Position {
	return Position{
		Ticker:    row.Ticker,
		Quantity:  row.Quantity,
		AvgPrice:  row.AvgPrice,
		UpdatedAt: row.UpdatedAt,
	}
}
