package mycel

import (
	"context"
	"log/slog"

	"github.com/conamu/mycorrizal/internal/nodosum"
)

type Config struct {
	Ctx     context.Context
	Logger  *slog.Logger
	Nodosum *nodosum.Nodosum
}
