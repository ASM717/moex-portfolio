package portfolio

import (
	"slices"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/ASM717/moex-portfolio/internal/moex"
)

// d — короткий хелпер для читаемых таблиц: d("250.5") вместо
// decimal.RequireFromString("250.5"). RequireFromString паникует на
// некорректной строке — в тестовых литералах это именно то, что нужно.
func d(s string) decimal.Decimal {
	return decimal.RequireFromString(s)
}

func TestValuePosition(t *testing.T) {
	tests := []struct {
		name        string
		pos         Position
		price       string
		wantCost    string
		wantValue   string
		wantPnL     string
		wantPercent string // "" — процента быть не должно (nil)
	}{
		{
			name: "profit", pos: Position{Quantity: 10, AvgPrice: d("250.5")}, price: "282.49",
			wantCost: "2505", wantValue: "2824.9", wantPnL: "319.9", wantPercent: "12.77",
		},
		{
			name: "loss", pos: Position{Quantity: 100, AvgPrice: d("130")}, price: "96.22",
			wantCost: "13000", wantValue: "9622", wantPnL: "-3378", wantPercent: "-25.98",
		},
		{
			name: "break even", pos: Position{Quantity: 1, AvgPrice: d("5.82")}, price: "5.82",
			wantCost: "5.82", wantValue: "5.82", wantPnL: "0", wantPercent: "0",
		},
		{
			// Классическая ловушка float: 0.1 * 3 = 0.30000000000000004.
			// На decimal результат точный.
			name: "exact decimal math", pos: Position{Quantity: 3, AvgPrice: d("0.1")}, price: "0.2",
			wantCost: "0.3", wantValue: "0.6", wantPnL: "0.3", wantPercent: "100",
		},
		{
			name: "free shares have no percent", pos: Position{Quantity: 5, AvgPrice: d("0")}, price: "10",
			wantCost: "0", wantValue: "50", wantPnL: "50", wantPercent: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := valuePosition(tt.pos, d(tt.price))

			// Сравниваем через Equal, а не строки: "2505" и "2505.0" — одно число.
			checks := []struct {
				field     string
				got, want decimal.Decimal
			}{
				{"cost", v.Cost, d(tt.wantCost)},
				{"value", v.Value, d(tt.wantValue)},
				{"pnl", v.PnL, d(tt.wantPnL)},
			}
			for _, c := range checks {
				if !c.got.Equal(c.want) {
					t.Errorf("%s = %s, want %s", c.field, c.got, c.want)
				}
			}

			switch {
			case tt.wantPercent == "" && v.PnLPercent != nil:
				t.Errorf("pnl percent = %s, want nil", v.PnLPercent)
			case tt.wantPercent != "" && v.PnLPercent == nil:
				t.Errorf("pnl percent = nil, want %s", tt.wantPercent)
			case tt.wantPercent != "" && !v.PnLPercent.Equal(d(tt.wantPercent)):
				t.Errorf("pnl percent = %s, want %s", v.PnLPercent, tt.wantPercent)
			}
		})
	}
}

func TestValuate(t *testing.T) {
	early := time.Date(2026, 10, 6, 15, 0, 0, 0, time.UTC)
	late := early.Add(5 * time.Minute)

	positions := []Position{
		{Ticker: "GAZP", Quantity: 100, AvgPrice: d("130")},
		{Ticker: "SBER", Quantity: 10, AvgPrice: d("250")},
		{Ticker: "SU26238RMFS4", Quantity: 3, AvgPrice: d("700")},
	}
	quotes := map[string]moex.Quote{
		"GAZP": {Ticker: "GAZP", Price: d("100"), Time: early},
		"SBER": {Ticker: "SBER", Price: d("300"), Time: late},
		// облигации нет в котировках
	}

	details, sum := valuate(positions, quotes)

	// Порядок позиций сохраняется, у облигации нет оценки.
	if len(details) != 3 {
		t.Fatalf("got %d positions, want 3", len(details))
	}
	if details[0].Valuation == nil || details[1].Valuation == nil {
		t.Error("GAZP and SBER must be valued")
	}
	if details[2].Valuation != nil {
		t.Errorf("bond must not be valued, got %+v", details[2].Valuation)
	}

	// Итоги — только по оценённым позициям:
	// cost  = 100*130 + 10*250 = 15500
	// value = 100*100 + 10*300 = 13000
	// pnl   = -2500, pct = -2500/15500*100 = -16.129... → -16.13
	if !sum.Cost.Equal(d("15500")) || !sum.Value.Equal(d("13000")) || !sum.PnL.Equal(d("-2500")) {
		t.Errorf("summary cost/value/pnl = %s/%s/%s, want 15500/13000/-2500", sum.Cost, sum.Value, sum.PnL)
	}
	if sum.PnLPercent == nil || !sum.PnLPercent.Equal(d("-16.13")) {
		t.Errorf("summary pnl percent = %v, want -16.13", sum.PnLPercent)
	}
	if !sum.PricedAt.Equal(late) {
		t.Errorf("priced at = %s, want the latest quote time %s", sum.PricedAt, late)
	}
	// slices.Equal — из стандартного пакета slices (Go 1.21+), аналог
	// List.equals. Слайсы через == сравнивать нельзя — это ошибка компиляции.
	if !slices.Equal(sum.Unpriced, []string{"SU26238RMFS4"}) {
		t.Errorf("unpriced = %v, want [SU26238RMFS4]", sum.Unpriced)
	}
}

func TestValuateEmpty(t *testing.T) {
	details, sum := valuate(nil, nil) // чтение из nil-map безопасно — вернёт "не найдено"

	if len(details) != 0 || !sum.Value.IsZero() || sum.PnLPercent != nil {
		t.Errorf("valuate(nil) = %v, %+v; want empty", details, sum)
	}
	// Unpriced должен быть пустым слайсом, а не nil — в JSON это [] против null.
	if sum.Unpriced == nil {
		t.Error("unpriced is nil, want empty slice")
	}
}
