package portfolio

import (
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// Portfolio — доменная модель портфеля.
//
// Это не то же самое, что db.Portfolio (сгенерированная sqlc структура-строка
// таблицы): доменные типы принадлежат бизнес-слою и не зависят от схемы БД.
// Java-параллель: разделение @Entity и доменной модели/DTO.
type Portfolio struct {
	ID        int64
	Name      string
	CreatedAt time.Time
}

// Position — позиция в портфеле: сколько штук бумаги и по какой средней цене.
type Position struct {
	Ticker   string
	Quantity int64
	// decimal.Decimal — аналог BigDecimal. Как и BigDecimal, он неизменяемый:
	// a.Add(b) возвращает новое значение. Сравнивать через a.Equal(b),
	// а не ==, потому что 1.0 и 1.00 — разные представления одного числа.
	AvgPrice  decimal.Decimal
	UpdatedAt time.Time
}

// Details — портфель вместе с позициями и их оценкой.
//
// Portfolio здесь ВСТРОЕН (embedding): поле без имени. Поля и методы
// Portfolio становятся доступны напрямую: d.Name вместо d.Portfolio.Name.
// Это НЕ наследование: Details нельзя передать туда, где ждут Portfolio,
// и нет никакого полиморфизма. Это композиция с синтаксическим сахаром
// ("composition over inheritance", доведённое до уровня языка).
type Details struct {
	Portfolio
	Positions []PositionDetails
	// Summary == nil, если котировки получить не удалось (ISS недоступен).
	// Портфель при этом всё равно отдаётся — без оценки.
	Summary *Summary
}

// ErrNotFound — "сторожевая" ошибка (sentinel error): заранее созданное
// значение, с которым сравнивают через errors.Is.
//
// Java-параллель: вместо иерархии исключений (EntityNotFoundException
// extends RuntimeException) в Go используют значения ошибок. Слои выше
// оборачивают её с контекстом: fmt.Errorf("portfolio %d: %w", id, ErrNotFound),
// а errors.Is(err, ErrNotFound) всё равно её найдёт, размотав цепочку.
var ErrNotFound = errors.New("not found")

// ValidationError — ошибка входных данных с указанием поля.
//
// Когда к ошибке нужно приложить данные, делают свой тип, реализующий
// интерфейс error (любой тип с методом Error() string — опять неявная
// реализация интерфейса). Достают такую ошибку через errors.As —
// аналог catch (ValidationException e) с доступом к полям e.
type ValidationError struct {
	Field   string
	Message string
}

// Error реализует интерфейс error.
//
// Получатель — указатель (*ValidationError). Значит, интерфейс error
// реализует именно *ValidationError, и создавать ошибку нужно через
// &ValidationError{...}. Смешивать значения и указатели для одного типа
// ошибок — частый источник багов с errors.As, поэтому выбираем одно.
func (e *ValidationError) Error() string {
	return fmt.Sprintf("invalid %s: %s", e.Field, e.Message)
}
