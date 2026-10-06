package portfolio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// fakeService — фейк бизнес-слоя для тестов хендлеров. Поведение каждого
// метода задаётся полем-функцией прямо в тесте — легковесная замена Mockito
// when(...).thenReturn(...). Незаданное поле — nil, и его вызов упадёт
// с паникой: значит, хендлер позвал то, чего тест не ожидал.
type fakeService struct {
	createPortfolio func(ctx context.Context, name string) (Portfolio, error)
	listPortfolios  func(ctx context.Context) ([]Portfolio, error)
	getPortfolio    func(ctx context.Context, id int64) (Details, error)
	setPosition     func(ctx context.Context, portfolioID int64, p Position) (Position, error)
	deletePosition  func(ctx context.Context, portfolioID int64, ticker string) error
}

func (f *fakeService) CreatePortfolio(ctx context.Context, name string) (Portfolio, error) {
	return f.createPortfolio(ctx, name)
}

func (f *fakeService) ListPortfolios(ctx context.Context) ([]Portfolio, error) {
	return f.listPortfolios(ctx)
}

func (f *fakeService) GetPortfolio(ctx context.Context, id int64) (Details, error) {
	return f.getPortfolio(ctx, id)
}

func (f *fakeService) SetPosition(ctx context.Context, portfolioID int64, p Position) (Position, error) {
	return f.setPosition(ctx, portfolioID, p)
}

func (f *fakeService) DeletePosition(ctx context.Context, portfolioID int64, ticker string) error {
	return f.deletePosition(ctx, portfolioID, ticker)
}

// Проверка на этапе компиляции, что *Service и *fakeService подходят под
// интерфейс service. Присваивание в "пустую" переменную _ ничего не делает
// в рантайме, но сломает сборку, если сигнатуры разойдутся.
// Частая идиома: Java-шное "implements", выраженное явно там, где это важно.
var (
	_ service = (*Service)(nil)
	_ service = (*fakeService)(nil)
)

// newTestServer собирает роутер с хендлером так же, как в main —
// тестируем вместе с маршрутизацией, включая разбор {id} и методов.
func newTestServer(svc service) http.Handler {
	mux := http.NewServeMux()
	NewHandler(svc).Register(mux)
	return mux
}

// do выполняет запрос к хендлеру без реального сетевого сервера.
// httptest.NewRecorder — ResponseWriter, записывающий ответ в память
// (аналог MockMvc в Spring).
func do(t *testing.T, h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestCreatePortfolio(t *testing.T) {
	created := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

	svc := &fakeService{
		createPortfolio: func(_ context.Context, name string) (Portfolio, error) {
			if name == "" {
				return Portfolio{}, &ValidationError{Field: "name", Message: "must not be empty"}
			}
			return Portfolio{ID: 7, Name: name, CreatedAt: created}, nil
		},
	}
	h := newTestServer(svc)

	t.Run("created", func(t *testing.T) {
		rec := do(t, h, http.MethodPost, "/portfolios", `{"name":"ИИС"}`)

		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusCreated, rec.Body)
		}
		if got := rec.Header().Get("Location"); got != "/portfolios/7" {
			t.Errorf("Location = %q, want %q", got, "/portfolios/7")
		}
		var resp portfolioResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		want := portfolioResponse{ID: 7, Name: "ИИС", CreatedAt: created}
		// Структуры из сравнимых полей можно сравнивать через == (time.Time
		// лучше сравнивать через Equal, но после JSON-круга в UTC это эквивалентно).
		if resp != want {
			t.Errorf("response = %+v, want %+v", resp, want)
		}
	})

	// Ошибочные запросы — одной таблицей: важен только статус.
	badRequests := []struct {
		name string
		body string
	}{
		{name: "validation error", body: `{"name":""}`},
		{name: "malformed json", body: `{"name":`},
		{name: "unknown field", body: `{"nmae":"typo"}`},
		{name: "two objects", body: `{"name":"a"}{"name":"b"}`},
		{name: "wrong type", body: `{"name":42}`},
	}
	for _, tt := range badRequests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, h, http.MethodPost, "/portfolios", tt.body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d; body: %s", rec.Code, http.StatusBadRequest, rec.Body)
			}
		})
	}
}

func TestGetPortfolio(t *testing.T) {
	svc := &fakeService{
		getPortfolio: func(_ context.Context, id int64) (Details, error) {
			switch id {
			case 1:
				return Details{
					Portfolio: Portfolio{ID: 1, Name: "Основной"},
					Positions: []Position{{Ticker: "SBER", Quantity: 10, AvgPrice: decimal.RequireFromString("250.5")}},
				}, nil
			case 500:
				return Details{}, errors.New("connection reset by peer")
			default:
				// Оборачиваем так же, как настоящий репозиторий, — проверяем,
				// что хендлер находит ErrNotFound через errors.Is в цепочке.
				return Details{}, fmt.Errorf("portfolio %d: %w", id, ErrNotFound)
			}
		},
	}
	h := newTestServer(svc)

	t.Run("found", func(t *testing.T) {
		rec := do(t, h, http.MethodGet, "/portfolios/1", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body)
		}
		// Проверяем форму JSON через map — так видно реальные ключи и то,
		// что avg_price сериализован строкой, а встроенная структура — плоско.
		var resp map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if resp["name"] != "Основной" {
			t.Errorf("name = %v, want %q", resp["name"], "Основной")
		}
		// Type assertion x.([]any) — приведение типа с проверкой (comma ok),
		// аналог instanceof + cast. Без ok при несовпадении была бы паника.
		positions, ok := resp["positions"].([]any)
		if !ok || len(positions) != 1 {
			t.Fatalf("positions = %v, want one element", resp["positions"])
		}
		pos := positions[0].(map[string]any)
		if pos["avg_price"] != "250.5" {
			t.Errorf("avg_price = %#v, want string %q", pos["avg_price"], "250.5")
		}
	})

	statusCases := []struct {
		name   string
		target string
		want   int
	}{
		{name: "not found", target: "/portfolios/2", want: http.StatusNotFound},
		{name: "non-numeric id", target: "/portfolios/abc", want: http.StatusBadRequest},
		{name: "negative id", target: "/portfolios/-1", want: http.StatusBadRequest},
		{name: "internal error hidden", target: "/portfolios/500", want: http.StatusInternalServerError},
	}
	for _, tt := range statusCases {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, h, http.MethodGet, tt.target, "")
			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d; body: %s", rec.Code, tt.want, rec.Body)
			}
			// Внутренние детали ошибки не должны утекать клиенту.
			if strings.Contains(rec.Body.String(), "connection reset") {
				t.Errorf("response leaks internal error: %s", rec.Body)
			}
		})
	}
}

func TestSetPosition(t *testing.T) {
	var got Position
	svc := &fakeService{
		setPosition: func(_ context.Context, _ int64, p Position) (Position, error) {
			got = p
			return p, nil
		},
	}
	h := newTestServer(svc)

	t.Run("ok", func(t *testing.T) {
		rec := do(t, h, http.MethodPut, "/portfolios/1/positions/SBER", `{"quantity":10,"avg_price":"250.50"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body)
		}
		if got.Ticker != "SBER" || got.Quantity != 10 || !got.AvgPrice.Equal(decimal.RequireFromString("250.5")) {
			t.Errorf("service got %+v", got)
		}
	})

	t.Run("price as json number", func(t *testing.T) {
		rec := do(t, h, http.MethodPut, "/portfolios/1/positions/SBER", `{"quantity":1,"avg_price":0.1}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body)
		}
		// 0.1 из JSON-числа должно прийти точно, без float-погрешности.
		if !got.AvgPrice.Equal(decimal.RequireFromString("0.1")) {
			t.Errorf("avg_price = %s, want 0.1", got.AvgPrice)
		}
	})

	t.Run("missing avg_price", func(t *testing.T) {
		rec := do(t, h, http.MethodPut, "/portfolios/1/positions/SBER", `{"quantity":10}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want %d; body: %s", rec.Code, http.StatusBadRequest, rec.Body)
		}
	})
}

func TestDeletePosition(t *testing.T) {
	svc := &fakeService{
		deletePosition: func(_ context.Context, _ int64, ticker string) error {
			if ticker == "SBER" {
				return nil
			}
			return ErrNotFound
		},
	}
	h := newTestServer(svc)

	if rec := do(t, h, http.MethodDelete, "/portfolios/1/positions/SBER", ""); rec.Code != http.StatusNoContent {
		t.Errorf("existing: status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if rec := do(t, h, http.MethodDelete, "/portfolios/1/positions/GAZP", ""); rec.Code != http.StatusNotFound {
		t.Errorf("missing: status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
