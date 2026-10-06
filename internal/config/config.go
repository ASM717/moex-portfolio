// Package config читает настройки приложения из переменных окружения.
//
// Java-параллель: аналог application.yml + @ConfigurationProperties, только
// без фреймворка. В Go-сервисах принято брать конфиг из env (12-factor app):
// в docker/k8s это самый простой способ, а локально выручают значения
// по умолчанию.
package config

import (
	"os"

	"github.com/ASM717/moex-portfolio/internal/moex"
)

// Config — все настройки сервиса в одном месте.
//
// struct в Go — это просто набор полей, без конструкторов, геттеров и
// наследования (аналог Java record, но изменяемый). Поля с заглавной буквы —
// экспортируемые (видны из других пакетов).
type Config struct {
	// HTTPAddr — адрес, на котором слушает HTTP-сервер, например ":8080".
	HTTPAddr string
	// DatabaseURL — строка подключения к PostgreSQL в формате URL,
	// например postgres://user:pass@host:5432/db?sslmode=disable.
	DatabaseURL string
	// ISSBaseURL — корень ISS API Мосбиржи. Переопределяется, например,
	// чтобы направить сервис на заглушку при ручном тестировании.
	ISSBaseURL string
}

// Load собирает Config из окружения.
//
// Возвращаем значение (Config), а не указатель (*Config): структура маленькая,
// копируется дёшево, и никто не сможет случайно изменить общий конфиг
// через указатель. В Java все объекты — ссылки; в Go ты сам выбираешь
// между значением и указателем.
//
// Значения по умолчанию совпадают с docker-compose.yml, поэтому локально
// сервис запускается без единой переменной: `go run ./cmd/server`.
func Load() Config {
	return Config{
		HTTPAddr:    getEnv("HTTP_ADDR", ":8080"),
		DatabaseURL: getEnv("DATABASE_URL", "postgres://moex:moex@localhost:5432/moex?sslmode=disable"),
		ISSBaseURL:  getEnv("ISS_BASE_URL", moex.DefaultBaseURL),
	}
}

// getEnv возвращает значение переменной окружения или fallback, если она
// не задана или пустая.
//
// Строчная буква в имени — функция видна только внутри пакета config.
func getEnv(key, fallback string) string {
	// Идиома "comma ok": многие операции в Go возвращают второе значение
	// bool — "нашлось ли". Здесь оно отличает "переменной нет" от
	// "переменная есть, но пустая". Тот же приём — у map и type assertion.
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
