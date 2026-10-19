package observability

import (
	"log/slog"
	"os"
	"strings"

	"github.com/KevVemn/junglegaming-backend/internal/config"
)

func NewLogger(cfg config.LogConfig) *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(cfg.Level) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	opts := &slog.HandlerOptions{Level: level}

	var handler slog.Handler = slog.NewJSONHandler(os.Stdout, opts)
	if strings.ToLower(cfg.Format) == "text" {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}

	return slog.New(handler)
}
