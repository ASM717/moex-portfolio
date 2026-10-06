package portfolio

import (
	"time"

	"github.com/shopspring/decimal"

	"github.com/ASM717/moex-portfolio/internal/moex"
)

// hundred — константа для процентов. decimal нельзя объявить через const
// (const в Go — только для чисел, строк и bool, вычисляемых при компиляции),
// поэтому это переменная уровня пакета. Менять её никто не должен.
var hundred = decimal.NewFromInt(100)

// Valuation — оценка позиции по текущей цене.
type Valuation struct {
	Price decimal.Decimal // текущая цена за штуку
	Cost  decimal.Decimal // сколько потрачено: avg_price * quantity
	Value decimal.Decimal // сколько стоит сейчас: price * quantity
	PnL   decimal.Decimal // прибыль/убыток: Value - Cost
	// PnLPercent — доходность в процентах, округлённая до сотых.
	// nil, если Cost == 0 (бумаги достались бесплатно): делить не на что,
	// а 0% или "бесконечность" были бы враньём. Указатель здесь — способ
	// сказать "значения нет" (в JSON превратится в null).
	PnLPercent *decimal.Decimal
}

// PositionDetails — позиция вместе с оценкой.
type PositionDetails struct {
	Position
	// Valuation == nil, если для тикера нет котировки
	// (бумага не торгуется на TQBR, облигация, неизвестный тикер).
	Valuation *Valuation
}

// Summary — итоги по портфелю.
type Summary struct {
	// Суммы считаются только по позициям, для которых есть котировка.
	Cost       decimal.Decimal
	Value      decimal.Decimal
	PnL        decimal.Decimal
	PnLPercent *decimal.Decimal
	// PricedAt — время самой свежей котировки (данные ISS отложены ~на 15 минут).
	PricedAt time.Time
	// Unpriced — тикеры, которые не удалось оценить и которые НЕ вошли в суммы.
	// Если список не пуст, итоги неполные — клиент должен это показать.
	Unpriced []string
}

// valuate оценивает позиции по котировкам.
//
// Чистая функция: никакого I/O, всё нужное приходит в аргументах,
// результат зависит только от них. Такие функции тестируются проще всего —
// таблица "вход → ожидаемый выход", без моков. Поэтому расчёт вынесен
// отдельно от сервиса, который ходит в БД и ISS.
func valuate(positions []Position, quotes map[string]moex.Quote) ([]PositionDetails, Summary) {
	details := make([]PositionDetails, 0, len(positions))
	// Нулевое значение decimal.Decimal — это 0, поэтому Summary{} сразу
	// готов к накоплению сумм. "Полезное нулевое значение" — важная идиома
	// Go: хорошо спроектированный тип работает без конструктора.
	// Unpriced инициализируем явно, чтобы в JSON был [], а не null.
	sum := Summary{Unpriced: []string{}}

	for _, p := range positions {
		q, ok := quotes[p.Ticker]
		if !ok {
			details = append(details, PositionDetails{Position: p})
			sum.Unpriced = append(sum.Unpriced, p.Ticker)
			continue
		}

		v := valuePosition(p, q.Price)
		// &v — указатель на локальную переменную. В Java так нельзя было бы
		// "вынести" ссылку на стековую переменную, а в Go можно: компилятор
		// сам решит разместить v в куче (escape analysis). Каждая итерация
		// цикла создаёт новую v (с Go 1.22), так что указатели не "склеятся".
		details = append(details, PositionDetails{Position: p, Valuation: &v})

		sum.Cost = sum.Cost.Add(v.Cost)
		sum.Value = sum.Value.Add(v.Value)
		if q.Time.After(sum.PricedAt) {
			sum.PricedAt = q.Time
		}
	}

	sum.PnL = sum.Value.Sub(sum.Cost)
	sum.PnLPercent = percent(sum.PnL, sum.Cost)
	return details, sum
}

// valuePosition считает оценку одной позиции.
// decimal неизменяемый: каждый Mul/Sub возвращает новое значение, как BigDecimal.
func valuePosition(p Position, price decimal.Decimal) Valuation {
	qty := decimal.NewFromInt(p.Quantity)
	cost := p.AvgPrice.Mul(qty)
	value := price.Mul(qty)
	pnl := value.Sub(cost)
	return Valuation{
		Price:      price,
		Cost:       cost,
		Value:      value,
		PnL:        pnl,
		PnLPercent: percent(pnl, cost),
	}
}

// percent возвращает part / whole * 100, округлённое до сотых,
// или nil, если whole не положительный.
func percent(part, whole decimal.Decimal) *decimal.Decimal {
	if !whole.IsPositive() {
		return nil
	}
	// decimal.Round округляет "половину от нуля" (half away from zero):
	// 1.005 → 1.01, -1.005 → -1.01. В Java это RoundingMode.HALF_UP.
	// Не путать с банковским округлением (HALF_EVEN) — для него есть RoundBank.
	p := part.Div(whole).Mul(hundred).Round(2)
	return &p
}
