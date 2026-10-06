package moex

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL — корень ISS API. Данные публичные, без ключа, отложенные
// примерно на 15 минут.
const DefaultBaseURL = "https://iss.moex.com/iss"

const (
	defaultTimeout = 10 * time.Second
	// maxResponseBytes — защита от неожиданно огромного ответа.
	maxResponseBytes = 10 << 20 // 10 MiB
	userAgent        = "moex-portfolio (+https://github.com/ASM717/moex-portfolio)"
)

// Client — HTTP-клиент к ISS. Безопасен для использования из нескольких
// горутин одновременно (как и http.Client внутри), создаётся один на приложение.
//
// Java-параллель: аналог RestClient/WebClient-обёртки над внешним API,
// только без Feign-магии — запросы собираются руками.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// Option настраивает Client при создании.
//
// Это паттерн "functional options" — идиоматичная замена Builder в Go:
// New() без аргументов даёт рабочие значения по умолчанию, а нужное
// переопределяется точечно: moex.New(moex.WithBaseURL(srv.URL)).
// Option — просто функция, которая меняет поля ещё не готового клиента.
type Option func(*Client)

// WithBaseURL задаёт корень API — в тестах сюда передают адрес httptest.Server.
func WithBaseURL(u string) Option {
	return func(c *Client) {
		// Убираем завершающий "/", чтобы не получить "//" при склейке путей.
		c.baseURL = strings.TrimRight(u, "/")
	}
}

// WithHTTPClient подменяет http.Client (свой транспорт, таймауты, прокси).
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		c.httpClient = hc
	}
}

// New создаёт клиент ISS.
//
// Важно: http.DefaultClient (и функции http.Get/http.Post) НЕ имеют
// таймаута — зависший сервер подвесит горутину навсегда. Поэтому свой
// http.Client с Timeout обязателен. Timeout покрывает весь запрос целиком:
// соединение, отправку и чтение тела.
func New(opts ...Option) *Client {
	c := &Client{
		baseURL:    DefaultBaseURL,
		httpClient: &http.Client{Timeout: defaultTimeout},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// get выполняет GET-запрос к ISS и возвращает запрошенные таблицы (blocks).
//
// Общие параметры ISS:
//   - iss.meta=off — не присылать описание типов колонок (меньше трафика);
//   - iss.only=<blocks> — прислать только нужные таблицы.
func (c *Client) get(ctx context.Context, path string, q url.Values, blocks ...string) (map[string]table, error) {
	q.Set("iss.meta", "off")
	q.Set("iss.only", strings.Join(blocks, ","))

	// url.Values.Encode экранирует параметры и сортирует их по ключу —
	// никакой ручной склейки строк с "&" и "=".
	u := c.baseURL + path + "?" + q.Encode()

	// NewRequestWithContext привязывает запрос к ctx: если контекст отменят
	// (клиент нашего API оборвал соединение, сервер останавливается),
	// запрос к ISS прервётся сразу, а не по таймауту.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", path, err)
	}
	// Тело ответа ОБЯЗАТЕЛЬНО закрывать, иначе соединение не вернётся в пул
	// и со временем кончатся файловые дескрипторы. Аналог try-with-resources
	// для InputStream ответа.
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Кусочек тела помогает понять причину, но читаем не больше 512 байт.
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("request %s: unexpected status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	tables, err := decodeTables(io.LimitReader(resp.Body, maxResponseBytes), blocks...)
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", path, err)
	}
	return tables, nil
}
