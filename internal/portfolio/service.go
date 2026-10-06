package portfolio

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const maxNameLength = 100

// tickerPattern — допустимый формат тикера (SECID в ISS): латиница в верхнем
// регистре и цифры, например SBER, GAZP, SU26238RMFS4.
//
// regexp.MustCompile паникует при некорректном выражении. Для переменных
// уровня пакета это нормально: ошибка в литерале всплывёт сразу при старте
// (и в любом тесте пакета), а не посреди запроса. Компилируем один раз,
// а не на каждый вызов, — как static final Pattern в Java.
var tickerPattern = regexp.MustCompile(`^[A-Z0-9]{1,20}$`)

// repository — то, что сервису нужно от хранилища.
//
// Интерфейс объявлен здесь, у потребителя, и он неэкспортируемый
// (строчная буква): это внутренняя деталь сервиса. *Repository подходит
// под него автоматически, а в тестах можно подставить фейк.
// Java-разработчик написал бы interface PortfolioRepository рядом
// с реализацией — в Go так делать не принято.
type repository interface {
	CreatePortfolio(ctx context.Context, name string) (Portfolio, error)
	GetPortfolio(ctx context.Context, id int64) (Portfolio, error)
	ListPortfolios(ctx context.Context) ([]Portfolio, error)
	ListPositions(ctx context.Context, portfolioID int64) ([]Position, error)
	UpsertPosition(ctx context.Context, portfolioID int64, p Position) (Position, error)
	DeletePosition(ctx context.Context, portfolioID int64, ticker string) error
}

// Service — бизнес-логика портфелей: валидация и нормализация входных
// данных, сборка ответов из нескольких запросов к хранилищу.
// Позже здесь появится расчёт стоимости и доходности по котировкам.
type Service struct {
	repo repository
}

// NewService создаёт сервис. Зависимость передаётся явно через параметр —
// это и есть constructor injection, только без контейнера.
func NewService(repo repository) *Service {
	return &Service{repo: repo}
}

// CreatePortfolio проверяет имя и создаёт портфель.
func (s *Service) CreatePortfolio(ctx context.Context, name string) (Portfolio, error) {
	name = strings.TrimSpace(name)
	if err := validateName(name); err != nil {
		return Portfolio{}, err
	}
	return s.repo.CreatePortfolio(ctx, name)
}

// ListPortfolios возвращает все портфели.
func (s *Service) ListPortfolios(ctx context.Context) ([]Portfolio, error) {
	return s.repo.ListPortfolios(ctx)
}

// GetPortfolio возвращает портфель вместе с позициями.
func (s *Service) GetPortfolio(ctx context.Context, id int64) (Details, error) {
	p, err := s.repo.GetPortfolio(ctx, id)
	if err != nil {
		// Ошибку из репозитория отдаём как есть: она уже несёт контекст
		// ("portfolio 42: not found"). Повторно оборачивать одной и той же
		// информацией — шум в логах.
		return Details{}, err
	}
	positions, err := s.repo.ListPositions(ctx, id)
	if err != nil {
		return Details{}, err
	}
	// Два запроса без общей транзакции: между ними состояние может
	// измениться. Для просмотра портфеля это приемлемо; если понадобится
	// строгая согласованность — обернём в транзакцию REPEATABLE READ.
	return Details{Portfolio: p, Positions: positions}, nil
}

// SetPosition создаёт или заменяет позицию в портфеле.
// Тикер нормализуется: пробелы по краям убираются, регистр — верхний.
func (s *Service) SetPosition(ctx context.Context, portfolioID int64, p Position) (Position, error) {
	p.Ticker = normalizeTicker(p.Ticker)
	if err := validatePosition(p); err != nil {
		return Position{}, err
	}
	return s.repo.UpsertPosition(ctx, portfolioID, p)
}

// DeletePosition удаляет позицию из портфеля.
func (s *Service) DeletePosition(ctx context.Context, portfolioID int64, ticker string) error {
	ticker = normalizeTicker(ticker)
	if err := validateTicker(ticker); err != nil {
		return err
	}
	return s.repo.DeletePosition(ctx, portfolioID, ticker)
}

func normalizeTicker(t string) string {
	return strings.ToUpper(strings.TrimSpace(t))
}

func validateName(name string) error {
	if name == "" {
		return &ValidationError{Field: "name", Message: "must not be empty"}
	}
	// len(s) в Go — длина в БАЙТАХ (строки — это UTF-8 байты), а не в
	// символах, как String.length() в Java. "Мой портфель" — 12 символов,
	// но 23 байта. Для подсчёта символов — utf8.RuneCountInString.
	if utf8.RuneCountInString(name) > maxNameLength {
		return &ValidationError{Field: "name", Message: fmt.Sprintf("must be at most %d characters", maxNameLength)}
	}
	return nil
}

func validateTicker(ticker string) error {
	if !tickerPattern.MatchString(ticker) {
		return &ValidationError{Field: "ticker", Message: "must be 1-20 latin letters or digits"}
	}
	return nil
}

func validatePosition(p Position) error {
	if err := validateTicker(p.Ticker); err != nil {
		return err
	}
	if p.Quantity <= 0 {
		return &ValidationError{Field: "quantity", Message: "must be positive"}
	}
	// Сравнение decimal — только методами: IsNegative, Cmp, Equal.
	if p.AvgPrice.IsNegative() {
		return &ValidationError{Field: "avg_price", Message: "must not be negative"}
	}
	return nil
}
