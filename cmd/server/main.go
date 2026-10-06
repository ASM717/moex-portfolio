// Command server — точка входа сервиса moex-portfolio.
//
// Java-параллель: это аналог класса с `public static void main` +
// `@SpringBootApplication`, но без магии. Spring сам сканирует бины и
// связывает их; в Go всё это делается руками прямо здесь, в main:
// создали логгер → создали пул БД → создали репозиторий → сервис → хендлер.
// Такой подход называют "composition root". Он многословнее, зато весь граф
// зависимостей виден в одном файле и проверяется компилятором, а не в рантайме.
//
// `package main` — особенный пакет: только из него собирается исполняемый
// файл, и в нём обязана быть функция main(). Имя каталога (cmd/server)
// становится именем бинарника при `go build ./cmd/server`.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	// Импорты своего модуля — по полному пути модуля из go.mod.
	// Относительных импортов ("../internal/config") в Go нет.
	"github.com/ASM717/moex-portfolio/internal/config"
	"github.com/ASM717/moex-portfolio/internal/moex"
	"github.com/ASM717/moex-portfolio/internal/portfolio"
	"github.com/ASM717/moex-portfolio/internal/postgres"
	"github.com/ASM717/moex-portfolio/migrations"
)

// main в Go не возвращает код выхода и не принимает args (они в os.Args).
// Идиома: держать main минимальным, а всю логику — в run(), которая
// возвращает error. Так проще обрабатывать ошибки (обычный `return err`
// вместо os.Exit в десяти местах) и проще тестировать.
func main() {
	// slog — стандартный структурированный логгер (с Go 1.21).
	// Аналог SLF4J + Logback с JSON-энкодером, но из коробки, без зависимостей.
	// Логи пишутся парами ключ-значение: slog.Info("msg", "key", value).
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	// Делаем логгер глобальным по умолчанию, чтобы slog.Info(...) в любом
	// пакете писал через него. Для учебного проекта ок; в больших проектах
	// логгер чаще передают явно, как зависимость.
	slog.SetDefault(logger)

	if err := run(); err != nil {
		slog.Error("server stopped with error", "err", err)
		os.Exit(1) // os.Exit не выполняет defer-ы — поэтому зовём его только здесь.
	}
}

func run() error {
	// context.Context — ключевая концепция Go, прямого аналога в Java нет.
	// Ближе всего: смесь отмены (как Future.cancel / Thread.interrupt),
	// дедлайна (таймаут) и request-scoped значений (как ThreadLocal, но явно).
	// Контекст передаётся первым аргументом во все функции с I/O; когда он
	// отменяется, все вложенные операции (HTTP, SQL) должны прерваться.
	//
	// NotifyContext вернёт контекст, который отменится при Ctrl+C (SIGINT)
	// или SIGTERM (так docker/k8s просят процесс завершиться).
	// В Spring это делает shutdown hook + graceful shutdown из коробки.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	// defer выполнится при выходе из функции — как finally / try-with-resources.
	// Несколько defer-ов выполняются в обратном порядке (LIFO).
	defer stop()

	cfg := config.Load()

	// --- Сборка зависимостей (composition root) ---
	// Порядок как у Spring при старте: инфраструктура → миграции → бизнес-слой → web.
	// Каждый шаг может упасть, и мы сразу выходим с понятной ошибкой.

	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	// defer сработает при любом выходе из run — и при ошибке ниже, и при штатной
	// остановке. Т.к. defer-ы выполняются LIFO, пул закроется последним —
	// уже после остановки HTTP-сервера, который им пользуется.
	defer pool.Close()
	slog.Info("connected to postgres")

	if err := migrations.Up(ctx, pool); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}

	// ServeMux — встроенный роутер. С Go 1.22 он понимает метод и
	// параметры пути: "GET /portfolios/{id}" → r.PathValue("id").
	// Это закрывает большую часть того, для чего раньше брали chi/gorilla,
	// и примерно соответствует @GetMapping("/portfolios/{id}").
	mux := http.NewServeMux()
	mux.Handle("GET /healthz", handleHealth(pool))

	// Ручная "сборка бинов": repository → service → handler.
	// Каждый слой получает зависимость через конструктор; *Repository и
	// *Service подходят под интерфейсы, объявленные в слое выше, неявно.
	moexClient := moex.New(moex.WithBaseURL(cfg.ISSBaseURL))
	portfolioRepo := portfolio.NewRepository(pool)
	portfolioSvc := portfolio.NewService(portfolioRepo, moexClient)
	portfolio.NewHandler(portfolioSvc).Register(mux)

	// http.Server создаём явно, а не через http.ListenAndServe(addr, mux):
	// у глобальной версии нет таймаутов, и медленный клиент может держать
	// соединение бесконечно. В Tomcat эти таймауты настроены за тебя,
	// в Go — ответственность твоя.
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second, // time.Duration — типизированное число наносекунд
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Канал — типизированная очередь для общения между горутинами.
	// Буфер 1, чтобы горутина могла записать ошибку и завершиться,
	// даже если никто её уже не читает (иначе — утечка горутины).
	errCh := make(chan error, 1)

	// `go f()` запускает функцию в горутине — лёгком потоке, которым
	// управляет рантайм Go (похоже на virtual threads из Java 21).
	// ListenAndServe блокирует, поэтому уводим его в фон.
	go func() {
		slog.Info("http server listening", "addr", srv.Addr)
		// ErrServerClosed — штатная ошибка после Shutdown, её не считаем сбоем.
		// errors.Is — сравнение с учётом обёрток (%w), аналог проверки
		// цепочки getCause() в Java.
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	// select ждёт первое из нескольких событий — тут либо сервер упал,
	// либо пришёл сигнал остановки. Аналога в Java нет; ближе всего
	// CompletableFuture.anyOf.
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		slog.Info("shutdown signal received")
	}

	// Graceful shutdown: перестаём принимать новые соединения и даём
	// текущим запросам до 10 секунд на завершение.
	// Новый контекст от Background, потому что ctx уже отменён.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		// %w оборачивает ошибку, сохраняя исходную (как new RuntimeException(msg, cause)).
		// Вызывающий код сможет достать её через errors.Is / errors.As.
		return fmt.Errorf("shutdown http server: %w", err)
	}
	slog.Info("server stopped gracefully")
	return nil
}

// pinger — всё, что нужно health-хендлеру от базы: уметь отвечать на Ping.
//
// Ключевой нюанс Go: интерфейсы реализуются НЕЯВНО. *pgxpool.Pool нигде не
// пишет "implements pinger" — у него просто есть метод Ping(ctx) error,
// и этого достаточно (структурная типизация, "утиная" — но проверяется
// компилятором). Поэтому интерфейс объявляет потребитель (здесь, рядом с
// хендлером), а не поставщик (pgx), и в нём ровно те методы, что нужны.
// В Java наоборот: интерфейс объявляет реализация, и он обычно "толстый".
//
// Бонус: в тесте вместо настоящей БД можно подсунуть любую структуру с
// методом Ping — без Mockito.
type pinger interface {
	Ping(ctx context.Context) error
}

// handleHealth возвращает хендлер, проверяющий, что сервис жив и база доступна.
//
// Это функция, которая возвращает функцию (замыкание): возвращаемый хендлер
// "захватывает" переменную db. Так в Go передают зависимости в хендлеры без
// DI-контейнера — вместо @Autowired поля в контроллере. Когда хендлеров
// станет много, их сгруппируют в структуру с полями-зависимостями
// (почти как Spring-контроллер с конструктором).
//
// http.HandlerFunc — это тип-функция с сигнатурой (ResponseWriter, *Request),
// у которого есть метод ServeHTTP, поэтому он удовлетворяет интерфейсу
// http.Handler. Да, в Go методы можно объявлять даже у функциональных типов.
func handleHealth(db pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// r.Context() отменится, если клиент оборвёт соединение. Дополнительно
		// ограничиваем проверку двумя секундами, чтобы health-check не висел.
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		// Хендлер пишет ответ напрямую в ResponseWriter — никаких
		// возвращаемых ResponseEntity. Порядок важен: сначала заголовки,
		// потом WriteHeader(статус), потом тело — после первой записи тела
		// заголовки менять уже поздно.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")

		if err := db.Ping(ctx); err != nil {
			// Детали ошибки — в лог, а не клиенту (там может быть адрес БД и т.п.).
			// slog.ErrorContext берёт значения из контекста, если логгер их понимает.
			slog.ErrorContext(ctx, "health check: database unavailable", "err", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("database unavailable"))
			return
		}

		w.WriteHeader(http.StatusOK)
		// Ошибку записи игнорируем осознанно (`_ =`): клиент мог отвалиться,
		// и сделать с этим уже ничего нельзя. Go требует явно показать,
		// что ошибка проигнорирована, а не забыта.
		_, _ = w.Write([]byte("ok"))
	}
}
