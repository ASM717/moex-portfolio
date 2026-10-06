// Пакет moex_test (с суффиксом _test) — "внешний" тест: видит только
// экспортируемый API пакета moex, как настоящий пользователь. Так мы
// проверяем контракт клиента, а не его внутренности. Go разрешает держать
// такие тесты в том же каталоге — это единственное исключение из правила
// "один каталог — один пакет".
package moex_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/ASM717/moex-portfolio/internal/moex"
)

// newISS поднимает фейковый ISS на случайном порту localhost.
//
// httptest.NewServer — настоящий HTTP-сервер, поэтому клиент проходит весь
// путь: сборку URL, заголовки, сеть, статус, разбор тела. Аналог WireMock
// или MockWebServer из OkHttp, но в стандартной библиотеке.
// t.Cleanup регистрирует остановку сервера по завершении теста (как @AfterEach).
func newISS(t *testing.T, handler http.HandlerFunc) *moex.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return moex.New(moex.WithBaseURL(srv.URL))
}

// serveFile отдаёт фикстуру из testdata/. Каталог testdata go build
// игнорирует — это стандартное место для тестовых данных.
func serveFile(t *testing.T, path string) http.HandlerFunc {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}
}

func TestQuotes(t *testing.T) {
	var gotReq *http.Request
	fixture := serveFile(t, "testdata/quotes.json")
	client := newISS(t, func(w http.ResponseWriter, r *http.Request) {
		gotReq = r
		fixture(w, r)
	})

	quotes, err := client.Quotes(context.Background(), []string{"GAZP", "SBER", "AMEZ", "EMPTY", "ZERO"})
	if err != nil {
		t.Fatalf("Quotes: %v", err)
	}

	// Проверяем, что запрос собран правильно.
	if gotReq.URL.Path != "/engines/stock/markets/shares/boards/TQBR/securities.json" {
		t.Errorf("path = %q", gotReq.URL.Path)
	}
	query := gotReq.URL.Query()
	for key, want := range map[string]string{
		"securities": "GAZP,SBER,AMEZ,EMPTY,ZERO",
		"iss.meta":   "off",
		"iss.only":   "securities,marketdata",
	} {
		if got := query.Get(key); got != want {
			t.Errorf("query %s = %q, want %q", key, got, want)
		}
	}

	wantTime := time.Date(2026, 10, 6, 15, 56, 3, 0, time.UTC) // 18:56:03 МСК == 15:56:03 UTC

	tests := []struct {
		ticker    string
		wantPrice string // "" — котировки быть не должно
	}{
		{ticker: "SBER", wantPrice: "282.49"}, // есть LAST
		{ticker: "GAZP", wantPrice: "96.18"},  // LAST null → LCURRENTPRICE
		{ticker: "AMEZ", wantPrice: "68.05"},  // всё в marketdata null → PREVPRICE
		{ticker: "ZERO", wantPrice: "12.5"},   // нули считаем "нет данных" → PREVPRICE
		{ticker: "EMPTY"},                     // цены нет нигде → котировки нет
	}
	for _, tt := range tests {
		t.Run(tt.ticker, func(t *testing.T) {
			q, ok := quotes[tt.ticker]
			if tt.wantPrice == "" {
				if ok {
					t.Fatalf("unexpected quote: %+v", q)
				}
				return
			}
			if !ok {
				t.Fatalf("no quote for %s", tt.ticker)
			}
			if !q.Price.Equal(decimal.RequireFromString(tt.wantPrice)) {
				t.Errorf("price = %s, want %s", q.Price, tt.wantPrice)
			}
			// ZERO без SYSTIME — время нулевое, это допустимо.
			if tt.ticker != "ZERO" && !q.Time.Equal(wantTime) {
				t.Errorf("time = %s, want %s", q.Time, wantTime)
			}
		})
	}
}

func TestQuotesKeepsExactDecimal(t *testing.T) {
	// 0.1 + 0.2 во float64 даёт 0.30000000000000004. Проверяем, что цена из
	// JSON не проходит через float64: значение с 17 значащими цифрами
	// должно дойти до decimal без искажений.
	client := newISS(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"securities": {"columns": ["SECID", "PREVPRICE"], "data": []},
			"marketdata": {"columns": ["SECID", "LAST", "LCURRENTPRICE", "MARKETPRICE", "SYSTIME"],
				"data": [["X", 1234567.8901234567, null, null, null]]}
		}`))
	})

	quotes, err := client.Quotes(context.Background(), []string{"X"})
	if err != nil {
		t.Fatalf("Quotes: %v", err)
	}
	if got := quotes["X"].Price.String(); got != "1234567.8901234567" {
		t.Errorf("price = %s, want exact 1234567.8901234567", got)
	}
}

func TestQuotesNoTickersSkipsRequest(t *testing.T) {
	client := newISS(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("unexpected request to ISS")
	})

	quotes, err := client.Quotes(context.Background(), nil)
	if err != nil || len(quotes) != 0 {
		t.Errorf("Quotes(nil) = %v, %v; want empty map, nil", quotes, err)
	}
}

func TestQuotesErrors(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{
			name: "server error",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "boom", http.StatusInternalServerError)
			},
		},
		{
			name: "malformed json",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"marketdata": [`))
			},
		},
		{
			name: "missing block",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"marketdata": {"columns": [], "data": []}}`))
			},
		},
		{
			name: "missing column",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{
					"securities": {"columns": ["SECID"], "data": [["SBER"]]},
					"marketdata": {"columns": ["SECID"], "data": [["SBER"]]}
				}`))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newISS(t, tt.handler)
			if _, err := client.Quotes(context.Background(), []string{"SBER"}); err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestQuotesRespectsContext(t *testing.T) {
	// Сервер "зависает" до отмены запроса. Клиент должен вернуть ошибку,
	// как только истечёт контекст, а не ждать свой 10-секундный таймаут.
	client := newISS(t, func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := client.Quotes(ctx, []string{"SBER"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded in chain", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Quotes returned after %s, context was ignored", elapsed)
	}
}
