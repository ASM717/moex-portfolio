package moex

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// sharesBoardPath — режим торгов TQBR ("Т+: Акции и ДР") на фондовом рынке.
// Один и тот же тикер может торговаться в нескольких режимах (например,
// SBER ещё и в SPEQ), поэтому запрашиваем конкретный режим, а не весь рынок.
// Сейчас поддерживаем только акции и фонды на TQBR; облигации живут на
// другом рынке (markets/bonds) и котируются в процентах от номинала.
const sharesBoardPath = "/engines/stock/markets/shares/boards/TQBR/securities.json"

// moscow — часовой пояс биржи. Москва живёт в UTC+3 без перехода на летнее
// время с 2014 года, поэтому достаточно фиксированного смещения.
// time.LoadLocation("Europe/Moscow") тоже сработает, но требует базы часовых
// поясов в системе — в минимальных docker-образах её может не быть.
var moscow = time.FixedZone("MSK", 3*60*60)

// Quote — текущая котировка бумаги.
type Quote struct {
	Ticker string
	// Price — лучшая доступная оценка текущей цены за одну бумагу, в рублях.
	Price decimal.Decimal
	// Time — момент, на который ISS сформировал данные (не реальное время:
	// публичные данные отложены примерно на 15 минут).
	Time time.Time
}

// Quotes возвращает текущие котировки для акций и фондов из режима TQBR.
//
// Результат — map по тикеру. Тикеров, которых нет на TQBR или по которым
// нет ни одной цены, в map не будет — отсутствие проверяют через
// q, ok := quotes["SBER"]. Это не ошибка: портфель может содержать бумаги,
// которые мы пока не умеем оценивать.
func (c *Client) Quotes(ctx context.Context, tickers []string) (map[string]Quote, error) {
	if len(tickers) == 0 {
		return map[string]Quote{}, nil
	}

	q := url.Values{}
	q.Set("securities", strings.Join(tickers, ","))
	// Запрашиваем только нужные колонки — ответ становится в разы меньше.
	q.Set("securities.columns", "SECID,PREVPRICE")
	q.Set("marketdata.columns", "SECID,LAST,LCURRENTPRICE,MARKETPRICE,SYSTIME")

	tables, err := c.get(ctx, sharesBoardPath, q, "securities", "marketdata")
	if err != nil {
		return nil, fmt.Errorf("get quotes: %w", err)
	}

	prevPrices, err := readPrevPrices(tables["securities"])
	if err != nil {
		return nil, fmt.Errorf("get quotes: %w", err)
	}

	md := tables["marketdata"]
	quotes := make(map[string]Quote, md.len())
	for i := range md.len() { // range по числу (Go 1.22+): i = 0..len-1, как for (int i = 0; i < n; i++)
		quote, ok, err := readQuote(md, i, prevPrices)
		if err != nil {
			return nil, fmt.Errorf("get quotes: %w", err)
		}
		if ok {
			quotes[quote.Ticker] = quote
		}
	}
	return quotes, nil
}

// readPrevPrices читает цены закрытия прошлого дня из таблицы securities —
// последний запасной вариант цены.
func readPrevPrices(t table) (map[string]decimal.Decimal, error) {
	out := make(map[string]decimal.Decimal, t.len())
	for i := range t.len() {
		ticker, err := t.text(i, "SECID")
		if err != nil {
			return nil, err
		}
		price, ok, err := t.number(i, "PREVPRICE")
		if err != nil {
			return nil, err
		}
		if ok {
			out[ticker] = price
		}
	}
	return out, nil
}

// priceColumns — колонки marketdata в порядке предпочтения:
//   - LAST — цена последней сделки; null, если сегодня сделок не было;
//   - LCURRENTPRICE — официальная текущая цена, которую считает биржа;
//   - MARKETPRICE — рыночная цена по методике биржи (за прошлый день).
//
// Если пусто всё — берём PREVPRICE из таблицы securities.
var priceColumns = []string{"LAST", "LCURRENTPRICE", "MARKETPRICE"}

// readQuote собирает котировку из строки i таблицы marketdata.
// ok == false, если цену определить не удалось.
func readQuote(md table, i int, prevPrices map[string]decimal.Decimal) (Quote, bool, error) {
	ticker, err := md.text(i, "SECID")
	if err != nil {
		return Quote{}, false, err
	}

	price, found := decimal.Decimal{}, false
	for _, col := range priceColumns {
		p, ok, err := md.number(i, col)
		if err != nil {
			return Quote{}, false, err
		}
		// Нулевую цену тоже пропускаем: у ISS 0 иногда означает "нет данных".
		if ok && p.IsPositive() {
			price, found = p, true
			break
		}
	}
	if !found {
		price, found = prevPrices[ticker]
	}
	if !found || !price.IsPositive() {
		return Quote{}, false, nil
	}

	sysTime, err := md.text(i, "SYSTIME")
	if err != nil {
		return Quote{}, false, err
	}
	// Формат времени в Go задаётся не шаблоном "yyyy-MM-dd HH:mm:ss",
	// а примером эталонной даты: Mon Jan 2 15:04:05 2006 (запоминается
	// как 01/02 03:04:05 PM '06 — числа 1, 2, 3, 4, 5, 6).
	// Пустое или кривое время не делаем фатальным: цена важнее.
	ts, err := time.ParseInLocation(time.DateTime, sysTime, moscow) // time.DateTime == "2006-01-02 15:04:05"
	if err != nil {
		ts = time.Time{}
	}

	return Quote{Ticker: ticker, Price: price, Time: ts}, true, nil
}
