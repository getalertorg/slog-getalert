package sloggetalert_test

import (
	"log/slog"
	"os"

	sloggetalert "github.com/getalertorg/slog-getalert"
	slogmulti "github.com/samber/slog-multi"
)

func Example() {
	logHandlers := []slog.Handler{
		slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}),
	}

	alertHandler := sloggetalert.Option{
		Level:       slog.LevelWarn,
		Endpoint:    "https://api.getalert.ru/v1/events",
		APIKey:      os.Getenv("GETALERT_API_KEY"),
		Source:      "//my-service",
		Type:        "log.record",
		Environment: "production",
	}.NewHandler()
	defer alertHandler.Close()

	logHandlers = append(logHandlers, alertHandler)

	log := slog.New(slogmulti.Fanout(logHandlers...))

	log.Info("this goes only to stdout")
	log.Warn("this goes to stdout AND getalert", "component", "auth")
	log.Error("this too", "err", "connection refused")
}
