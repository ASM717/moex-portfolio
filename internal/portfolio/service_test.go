package portfolio

// Тесты в Go — обычные функции TestXxx(t *testing.T) в файлах *_test.go,
// которые не попадают в итоговый бинарник. Запуск: go test ./...
// Аналог JUnit встроен в язык; assert-библиотеки нет — проверки пишутся
// обычными if + t.Errorf (или берут testify, но стандарт вполне достаточен).
//
// Файл в том же пакете (package portfolio), поэтому видит неэкспортируемые
// имена — как тест в том же Java-пакете видит package-private.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/ASM717/moex-portfolio/internal/moex"
)

// fakeRepo — ручной фейк вместо мока. Встраивает интерфейс repository:
// так fakeRepo автоматически "реализует" все его методы, а переопределяем
// только нужные. Вызов не переопределённого метода — паника (nil-интерфейс),
// и тест сразу покажет, что сервис сделал неожиданный вызов.
type fakeRepo struct {
	repository

	createdName string
	upserted    Position
	deleted     string
	positions   []Position
}

func (f *fakeRepo) GetPortfolio(_ context.Context, id int64) (Portfolio, error) {
	return Portfolio{ID: id, Name: "test"}, nil
}

func (f *fakeRepo) ListPositions(_ context.Context, _ int64) ([]Position, error) {
	return f.positions, nil
}

func (f *fakeRepo) CreatePortfolio(_ context.Context, name string) (Portfolio, error) {
	f.createdName = name
	return Portfolio{ID: 1, Name: name}, nil
}

func (f *fakeRepo) UpsertPosition(_ context.Context, _ int64, p Position) (Position, error) {
	f.upserted = p
	return p, nil
}

func (f *fakeRepo) DeletePosition(_ context.Context, _ int64, ticker string) error {
	f.deleted = ticker
	return nil
}

func TestServiceCreatePortfolio(t *testing.T) {
	// Табличный тест (table-driven test) — главный приём тестирования в Go,
	// аналог @ParameterizedTest. Каждый случай — элемент слайса анонимных структур.
	tests := []struct {
		name      string
		input     string
		wantName  string
		wantField string // поле ValidationError; пусто — ошибки быть не должно
	}{
		{name: "valid", input: "Долгосрок", wantName: "Долгосрок"},
		{name: "trims spaces", input: "  ИИС  ", wantName: "ИИС"},
		{name: "empty", input: "", wantField: "name"},
		{name: "only spaces", input: "   ", wantField: "name"},
		// 100 кириллических символов = 200 байт: проверяем, что считаем символы, а не байты.
		{name: "100 cyrillic chars ok", input: strings.Repeat("я", 100), wantName: strings.Repeat("я", 100)},
		{name: "too long", input: strings.Repeat("я", 101), wantField: "name"},
	}

	for _, tt := range tests {
		// t.Run создаёт подтест с именем: в выводе будет
		// TestServiceCreatePortfolio/trims_spaces, и его можно запустить отдельно:
		// go test -run 'TestServiceCreatePortfolio/trims' ./internal/portfolio
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeRepo{}
			svc := NewService(repo, nil) // котировки в этом тесте не нужны: nil-интерфейс

			p, err := svc.CreatePortfolio(context.Background(), tt.input)

			if tt.wantField != "" {
				assertValidationError(t, err, tt.wantField)
				return
			}
			if err != nil {
				// t.Fatalf прерывает текущий (под)тест, t.Errorf — отмечает
				// провал и продолжает, чтобы показать все расхождения сразу.
				t.Fatalf("unexpected error: %v", err)
			}
			if p.Name != tt.wantName || repo.createdName != tt.wantName {
				t.Errorf("name = %q, saved %q; want %q", p.Name, repo.createdName, tt.wantName)
			}
		})
	}
}

func TestServiceSetPosition(t *testing.T) {
	price := decimal.RequireFromString("250.50")

	tests := []struct {
		name       string
		input      Position
		wantTicker string
		wantField  string
	}{
		{name: "valid", input: Position{Ticker: "SBER", Quantity: 10, AvgPrice: price}, wantTicker: "SBER"},
		{name: "normalizes ticker", input: Position{Ticker: " sber ", Quantity: 10, AvgPrice: price}, wantTicker: "SBER"},
		{name: "bond secid", input: Position{Ticker: "SU26238RMFS4", Quantity: 1, AvgPrice: price}, wantTicker: "SU26238RMFS4"},
		{name: "zero price ok", input: Position{Ticker: "SBER", Quantity: 1, AvgPrice: decimal.Zero}, wantTicker: "SBER"},
		{name: "empty ticker", input: Position{Ticker: "", Quantity: 10, AvgPrice: price}, wantField: "ticker"},
		{name: "bad ticker chars", input: Position{Ticker: "SB-ER", Quantity: 10, AvgPrice: price}, wantField: "ticker"},
		{name: "zero quantity", input: Position{Ticker: "SBER", Quantity: 0, AvgPrice: price}, wantField: "quantity"},
		{name: "negative quantity", input: Position{Ticker: "SBER", Quantity: -1, AvgPrice: price}, wantField: "quantity"},
		{name: "negative price", input: Position{Ticker: "SBER", Quantity: 1, AvgPrice: decimal.RequireFromString("-0.01")}, wantField: "avg_price"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeRepo{}
			svc := NewService(repo, nil) // котировки в этом тесте не нужны: nil-интерфейс

			_, err := svc.SetPosition(context.Background(), 1, tt.input)

			if tt.wantField != "" {
				assertValidationError(t, err, tt.wantField)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if repo.upserted.Ticker != tt.wantTicker {
				t.Errorf("saved ticker = %q, want %q", repo.upserted.Ticker, tt.wantTicker)
			}
			if !repo.upserted.AvgPrice.Equal(tt.input.AvgPrice) {
				t.Errorf("saved price = %s, want %s", repo.upserted.AvgPrice, tt.input.AvgPrice)
			}
		})
	}
}

func TestServiceDeletePositionNormalizesTicker(t *testing.T) {
	repo := &fakeRepo{}
	svc := NewService(repo, nil) // котировки в этом тесте не нужны: nil-интерфейс

	if err := svc.DeletePosition(context.Background(), 1, "gazp"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.deleted != "GAZP" {
		t.Errorf("deleted ticker = %q, want %q", repo.deleted, "GAZP")
	}
}

// assertValidationError — хелпер для тестов. t.Helper() говорит фреймворку,
// что при падении нужно показывать строку вызова хелпера, а не строку внутри него.
func assertValidationError(t *testing.T, err error, wantField string) {
	t.Helper()
	var vErr *ValidationError
	if !errors.As(err, &vErr) {
		t.Fatalf("error = %v, want *ValidationError", err)
	}
	if vErr.Field != wantField {
		t.Errorf("validation field = %q, want %q", vErr.Field, wantField)
	}
}

// fakeQuoter отдаёт заранее заданные котировки или ошибку и запоминает,
// сколько раз его вызвали.
type fakeQuoter struct {
	quotes map[string]moex.Quote
	err    error
	calls  int
}

func (f *fakeQuoter) Quotes(_ context.Context, _ []string) (map[string]moex.Quote, error) {
	f.calls++
	return f.quotes, f.err
}

func TestServiceGetPortfolio(t *testing.T) {
	sber := Position{Ticker: "SBER", Quantity: 10, AvgPrice: decimal.RequireFromString("250")}

	t.Run("valued", func(t *testing.T) {
		repo := &fakeRepo{positions: []Position{sber}}
		quotes := &fakeQuoter{quotes: map[string]moex.Quote{
			"SBER": {Ticker: "SBER", Price: decimal.RequireFromString("300")},
		}}

		d, err := NewService(repo, quotes).GetPortfolio(context.Background(), 1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if d.Summary == nil {
			t.Fatal("summary is nil, want valuation")
		}
		if want := decimal.RequireFromString("3000"); !d.Summary.Value.Equal(want) {
			t.Errorf("summary value = %s, want %s", d.Summary.Value, want)
		}
		if d.Positions[0].Valuation == nil {
			t.Error("position valuation is nil")
		}
	})

	t.Run("quotes unavailable", func(t *testing.T) {
		repo := &fakeRepo{positions: []Position{sber}}
		quotes := &fakeQuoter{err: errors.New("iss is down")}

		d, err := NewService(repo, quotes).GetPortfolio(context.Background(), 1)
		// Ошибка ISS не должна превращаться в ошибку запроса.
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if d.Summary != nil {
			t.Errorf("summary = %+v, want nil", d.Summary)
		}
		if len(d.Positions) != 1 || d.Positions[0].Valuation != nil {
			t.Errorf("positions = %+v, want one position without valuation", d.Positions)
		}
	})

	t.Run("empty portfolio skips ISS", func(t *testing.T) {
		quotes := &fakeQuoter{}

		d, err := NewService(&fakeRepo{}, quotes).GetPortfolio(context.Background(), 1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if quotes.calls != 0 {
			t.Errorf("ISS called %d times, want 0", quotes.calls)
		}
		if d.Summary == nil || !d.Summary.Value.IsZero() {
			t.Errorf("summary = %+v, want zero valuation", d.Summary)
		}
	})
}
