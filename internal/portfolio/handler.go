package portfolio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/shopspring/decimal"
)

// maxBodyBytes — ограничение размера тела запроса. В Spring/Tomcat лимит
// задан за тебя; в net/http тело по умолчанию не ограничено, и клиент
// может прислать гигабайт.
const maxBodyBytes = 1 << 20 // 1 MiB

// service — то, что хендлерам нужно от бизнес-слоя. *Service подходит
// автоматически, а в тестах хендлеров подставляется фейк.
type service interface {
	CreatePortfolio(ctx context.Context, name string) (Portfolio, error)
	ListPortfolios(ctx context.Context) ([]Portfolio, error)
	GetPortfolio(ctx context.Context, id int64) (Details, error)
	SetPosition(ctx context.Context, portfolioID int64, p Position) (Position, error)
	DeletePosition(ctx context.Context, portfolioID int64, ticker string) error
}

// Handler — HTTP-слой портфелей. Аналог @RestController: разбирает запрос,
// зовёт сервис, превращает результат или ошибку в HTTP-ответ.
// Бизнес-логики здесь нет.
type Handler struct {
	svc service
}

// NewHandler создаёт HTTP-хендлер портфелей.
func NewHandler(svc service) *Handler {
	return &Handler{svc: svc}
}

// Register регистрирует маршруты в роутере. Аналог @RequestMapping на
// методах контроллера, только все маршруты видны в одном месте.
//
// h.createPortfolio без скобок — это "method value": функция, уже
// привязанная к h (как this::createPortfolio в Java).
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /portfolios", h.createPortfolio)
	mux.HandleFunc("GET /portfolios", h.listPortfolios)
	mux.HandleFunc("GET /portfolios/{id}", h.getPortfolio)
	mux.HandleFunc("PUT /portfolios/{id}/positions/{ticker}", h.setPosition)
	mux.HandleFunc("DELETE /portfolios/{id}/positions/{ticker}", h.deletePosition)
}

// --- DTO: форма JSON на входе и выходе ---
//
// Отдельные типы для JSON, а не доменные структуры с json-тегами: так
// формат API не зависит от внутренней модели (как Request/Response DTO
// в Spring). Тег `json:"name"` — аналог @JsonProperty("name").
// Типы неэкспортируемые — они нужны только этому файлу.

type createPortfolioRequest struct {
	Name string `json:"name"`
}

type setPositionRequest struct {
	Quantity int64 `json:"quantity"`
	// Указатель, чтобы отличить "поле не передали" (nil) от "передали 0".
	// Для обычного decimal.Decimal оба случая выглядели бы одинаково —
	// нулевым значением. Так в Go выражают Optional для полей JSON.
	AvgPrice *decimal.Decimal `json:"avg_price"`
}

type portfolioResponse struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"` // time.Time сериализуется в RFC 3339
}

type positionResponse struct {
	Ticker   string `json:"ticker"`
	Quantity int64  `json:"quantity"`
	// decimal.Decimal сериализуется в JSON строкой ("250.5"), а не числом:
	// так JS-клиенты не потеряют точность на float64. На входе принимаются
	// и строка, и число.
	AvgPrice  decimal.Decimal `json:"avg_price"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// valuationResponse — оценка позиции. Суммы в рублях округляются до копеек
// при выводе; внутри расчёт ведётся точно, без округлений.
type valuationResponse struct {
	CurrentPrice decimal.Decimal  `json:"current_price"`
	Cost         decimal.Decimal  `json:"cost"`
	MarketValue  decimal.Decimal  `json:"market_value"`
	PnL          decimal.Decimal  `json:"pnl"`
	PnLPercent   *decimal.Decimal `json:"pnl_percent"` // nil → null
}

// positionDetailsResponse — позиция с оценкой. Встраивание positionResponse
// поднимает его поля на верхний уровень JSON-объекта позиции.
type positionDetailsResponse struct {
	positionResponse
	// Указатель на структуру: nil сериализуется в "valuation": null —
	// клиент явно видит, что оценки нет (а не нулевые суммы).
	Valuation *valuationResponse `json:"valuation"`
}

type summaryResponse struct {
	Cost        decimal.Decimal  `json:"cost"`
	MarketValue decimal.Decimal  `json:"market_value"`
	PnL         decimal.Decimal  `json:"pnl"`
	PnLPercent  *decimal.Decimal `json:"pnl_percent"`
	// *time.Time, чтобы при отсутствии времени отдать null, а не
	// "0001-01-01T00:00:00Z" — так сериализуется нулевой time.Time.
	PricedAt *time.Time `json:"priced_at"`
	Unpriced []string   `json:"unpriced"`
}

// portfolioDetailsResponse встраивает portfolioResponse: encoding/json
// "поднимает" поля встроенной структуры на верхний уровень, поэтому
// в JSON будет плоский объект {id, name, created_at, positions, summary}.
type portfolioDetailsResponse struct {
	portfolioResponse
	Positions []positionDetailsResponse `json:"positions"`
	// nil, если котировки недоступны.
	Summary *summaryResponse `json:"summary"`
}

type errorResponse struct {
	Error string `json:"error"`
}

// --- Хендлеры ---

func (h *Handler) createPortfolio(w http.ResponseWriter, r *http.Request) {
	var req createPortfolioRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	p, err := h.svc.CreatePortfolio(r.Context(), req.Name)
	if err != nil {
		writeError(w, r, err)
		return
	}

	// REST-конвенция для 201 Created: Location указывает на новый ресурс.
	w.Header().Set("Location", fmt.Sprintf("/portfolios/%d", p.ID))
	writeJSON(w, http.StatusCreated, toPortfolioResponse(p))
}

func (h *Handler) listPortfolios(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.ListPortfolios(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]portfolioResponse, 0, len(list))
	for _, p := range list {
		out = append(out, toPortfolioResponse(p))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) getPortfolio(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}

	d, err := h.svc.GetPortfolio(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, toDetailsResponse(d))
}

func (h *Handler) setPosition(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}

	var req setPositionRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	if req.AvgPrice == nil {
		writeError(w, r, &ValidationError{Field: "avg_price", Message: "is required"})
		return
	}

	pos, err := h.svc.SetPosition(r.Context(), id, Position{
		Ticker:   r.PathValue("ticker"),
		Quantity: req.Quantity,
		AvgPrice: *req.AvgPrice, // разыменование указателя — копия значения
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toPositionResponse(pos))
}

func (h *Handler) deletePosition(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}

	if err := h.svc.DeletePosition(r.Context(), id, r.PathValue("ticker")); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Вспомогательные функции ---

// pathID достаёт {id} из пути. r.PathValue — часть роутинга Go 1.22+,
// аналог @PathVariable, но без автоматического преобразования типа.
func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, &ValidationError{Field: "id", Message: "must be a positive integer"}
	}
	return id, nil
}

// decodeJSON читает тело запроса в dst строго: неизвестные поля, лишние
// данные после объекта и слишком большое тело — ошибка.
//
// dst имеет тип any (синоним interface{}) — аналог Object. Передавать нужно
// указатель (&req), иначе декодеру некуда записать результат.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	// По умолчанию encoding/json молча игнорирует неизвестные поля.
	// Строгий режим ловит опечатки клиента ("nmae" вместо "name") —
	// как FAIL_ON_UNKNOWN_PROPERTIES в Jackson.
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	// Decode читает только первое JSON-значение. Если после него есть
	// что-то ещё ({"a":1}{"b":2}), считаем тело некорректным.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("invalid JSON body: must contain a single JSON object")
	}
	return nil
}

// writeError превращает ошибку в HTTP-ответ — аналог @ControllerAdvice
// с @ExceptionHandler, только это обычная функция с явным switch.
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	var vErr *ValidationError
	switch {
	case errors.As(err, &vErr):
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: vErr.Error()})
	case errors.Is(err, ErrNotFound):
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "not found"})
	default:
		// Неожиданная ошибка: подробности — в лог, клиенту — общий текст,
		// чтобы не раскрывать детали внутреннего устройства (SQL, адреса).
		slog.ErrorContext(r.Context(), "request failed",
			"method", r.Method,
			"path", r.URL.Path,
			"err", err,
		)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal error"})
	}
}

// writeJSON пишет v как JSON с указанным статусом.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Заголовок со статусом уже отправлен — поменять ответ нельзя,
		// остаётся только залогировать.
		slog.Error("write json response", "err", err)
	}
}

// Мапперы домен → DTO.
//
// staticcheck (S1016) подсказывает: раз набор полей совпадает, можно написать
// portfolioResponse(p) — в Go структуры с одинаковыми полями конвертируются
// напрямую. Мы сознательно не делаем этого: явный маппинг отвязывает формат
// API от доменной модели. Новое поле в Portfolio не попадёт в JSON само.
// Директива //lint:ignore отключает проверку для одной строки с объяснением.

func toPortfolioResponse(p Portfolio) portfolioResponse {
	//lint:ignore S1016 explicit mapping keeps API decoupled from the domain model
	return portfolioResponse{ID: p.ID, Name: p.Name, CreatedAt: p.CreatedAt}
}

func toPositionResponse(p Position) positionResponse {
	//lint:ignore S1016 explicit mapping keeps API decoupled from the domain model
	return positionResponse{
		Ticker:    p.Ticker,
		Quantity:  p.Quantity,
		AvgPrice:  p.AvgPrice,
		UpdatedAt: p.UpdatedAt,
	}
}

// moneyPlaces — до скольких знаков округляем суммы в рублях при выводе.
const moneyPlaces = 2

func toDetailsResponse(d Details) portfolioDetailsResponse {
	resp := portfolioDetailsResponse{
		portfolioResponse: toPortfolioResponse(d.Portfolio),
		Positions:         make([]positionDetailsResponse, 0, len(d.Positions)),
	}
	for _, pos := range d.Positions {
		item := positionDetailsResponse{positionResponse: toPositionResponse(pos.Position)}
		if v := pos.Valuation; v != nil { // if с инициализатором: v виден только внутри if
			item.Valuation = &valuationResponse{
				CurrentPrice: v.Price,
				Cost:         v.Cost.Round(moneyPlaces),
				MarketValue:  v.Value.Round(moneyPlaces),
				PnL:          v.PnL.Round(moneyPlaces),
				PnLPercent:   v.PnLPercent,
			}
		}
		resp.Positions = append(resp.Positions, item)
	}

	if s := d.Summary; s != nil {
		resp.Summary = &summaryResponse{
			Cost:        s.Cost.Round(moneyPlaces),
			MarketValue: s.Value.Round(moneyPlaces),
			PnL:         s.PnL.Round(moneyPlaces),
			PnLPercent:  s.PnLPercent,
			Unpriced:    s.Unpriced,
		}
		if !s.PricedAt.IsZero() {
			// Копия в локальную переменную, чтобы взять адрес именно её,
			// а не поля доменной структуры.
			t := s.PricedAt
			resp.Summary.PricedAt = &t
		}
	}
	return resp
}
