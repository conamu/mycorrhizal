package mycorrizal

import (
	"log/slog"
	"os"
)

type Config struct {
	logger *slog.Logger
}

func GetDefaultConfig() *Config {
	return &Config{
		logger: slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
			Level: slog.LevelInfo,
		})),
	}
}
