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

	// ServeMux — встроенный роутер. С Go 1.22 он понимает метод и
	// параметры пути: "GET /portfolios/{id}" → r.PathValue("id").
	// Это закрывает большую часть того, для чего раньше брали chi/gorilla,
	// и примерно соответствует @GetMapping("/portfolios/{id}").
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealth)

	// http.Server создаём явно, а не через http.ListenAndServe(addr, mux):
	// у глобальной версии нет таймаутов, и медленный клиент может держать
	// соединение бесконечно. В Tomcat эти таймауты настроены за тебя,
	// в Go — ответственность твоя.
	srv := &http.Server{
		Addr:              ":8080",
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

// handleHealth — простейший хендлер для проверки, что сервис жив.
//
// Сигнатура (http.ResponseWriter, *http.Request) — стандарт для всех
// хендлеров. Никаких аннотаций и возвращаемых ResponseEntity: статус и тело
// пишутся напрямую в ResponseWriter. Порядок важен: сначала заголовки,
// потом WriteHeader(статус), потом тело — после первой записи тела
// заголовки менять уже поздно.
//
// Функция с маленькой буквы — значит не экспортируется (видна только
// внутри пакета). В Go это единственный модификатор доступа:
// Заглавная = public, строчная = package-private.
func handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	// Ошибку записи игнорируем осознанно (`_ =`): клиент мог отвалиться,
	// и сделать с этим уже ничего нельзя. Go требует явно показать,
	// что ошибка проигнорирована, а не забыта.
	_, _ = w.Write([]byte("ok"))
}
