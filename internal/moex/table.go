package moex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/shopspring/decimal"
)

// ISS отвечает не массивом объектов, а "таблицами": имена колонок отдельно,
// строки — массивами значений в том же порядке:
//
//	{"marketdata": {"columns": ["SECID", "LAST"], "data": [["SBER", 282.49], ["GAZP", null]]}}
//
// Поэтому стандартный json.Unmarshal в []struct{...} не подходит —
// нужен свой слой: разобрать таблицу и обращаться к ячейкам по имени колонки.
// Java-параллель: похоже на работу с ResultSet — rs.getString("SECID")
// превращается в t.text(i, "SECID").

// rawTable — форма одной таблицы в JSON.
//
// Ячейки читаем как json.RawMessage — "сырые" байты значения без разбора.
// Это ключевой момент для денег: если декодировать в any, encoding/json
// превратит 282.49 в float64 и точность будет потеряна ещё до нашего кода.
// Из сырых байт "282.49" decimal строится точно.
type rawTable struct {
	Columns []string            `json:"columns"`
	Data    [][]json.RawMessage `json:"data"`
}

// table — разобранная таблица с быстрым поиском колонки по имени.
type table struct {
	name string
	// map в Go — аналог HashMap. Нулевое значение map — nil: читать из него
	// можно (вернёт нулевое значение), а писать — паника. Поэтому создаём через make.
	cols map[string]int
	rows [][]json.RawMessage
}

// decodeTables читает ответ ISS и возвращает запрошенные таблицы по именам.
// Отсутствие любой из них — ошибка: значит, формат ответа не тот, что мы ждём.
//
// names ...string — вариадический параметр, как String... names в Java;
// внутри функции это обычный слайс []string.
func decodeTables(r io.Reader, names ...string) (map[string]table, error) {
	var raw map[string]rawTable
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode iss response: %w", err)
	}

	out := make(map[string]table, len(names))
	for _, name := range names {
		rt, ok := raw[name]
		if !ok {
			return nil, fmt.Errorf("iss response has no %q block", name)
		}
		t := table{name: name, cols: make(map[string]int, len(rt.Columns)), rows: rt.Data}
		for i, c := range rt.Columns {
			t.cols[c] = i
		}
		out[name] = t
	}
	return out, nil
}

// len возвращает количество строк. Метод у значения (t table), а не
// у указателя: таблица не меняется, а сама структура маленькая
// (map и slice внутри — это заголовки-ссылки, данные не копируются).
func (t table) len() int {
	return len(t.rows)
}

// cell возвращает сырое значение ячейки в строке i и колонке col.
// Неизвестная колонка — ошибка: ISS поменял формат или мы опечатались
// в списке колонок запроса. Лучше упасть явно, чем молча вернуть пустоту.
func (t table) cell(i int, col string) (json.RawMessage, error) {
	j, ok := t.cols[col]
	if !ok {
		return nil, fmt.Errorf("%s: no column %q", t.name, col)
	}
	row := t.rows[i]
	if j >= len(row) {
		return nil, fmt.Errorf("%s: row %d is shorter than columns", t.name, i)
	}
	return row[j], nil
}

// text читает строковую ячейку. null превращается в "".
func (t table) text(i int, col string) (string, error) {
	raw, err := t.cell(i, col)
	if err != nil {
		return "", err
	}
	if isNull(raw) {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("%s: row %d column %s: %w", t.name, i, col, err)
	}
	return s, nil
}

// number читает числовую ячейку точно.
//
// Возвращает три значения: число, флаг "значение есть" (false для null)
// и ошибку. Флаг нужен, потому что у decimal.Decimal нет "пустого"
// состояния — без него null и 0 были бы неотличимы. В Java здесь был бы
// BigDecimal, который может быть null; в Go значение не бывает nil,
// и отсутствие выражают отдельным bool (идиома "comma ok").
func (t table) number(i int, col string) (decimal.Decimal, bool, error) {
	raw, err := t.cell(i, col)
	if err != nil {
		return decimal.Decimal{}, false, err
	}
	if isNull(raw) {
		return decimal.Decimal{}, false, nil
	}
	d, err := decimal.NewFromString(string(raw))
	if err != nil {
		return decimal.Decimal{}, false, fmt.Errorf("%s: row %d column %s: %w", t.name, i, col, err)
	}
	return d, true, nil
}

func isNull(raw json.RawMessage) bool {
	return len(raw) == 0 || bytes.Equal(raw, []byte("null"))
}
