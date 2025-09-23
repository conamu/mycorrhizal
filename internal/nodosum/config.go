package nodosum

import (
	"context"
	"log/slog"
	"sync"
)

type Config struct {
	Ctx        context.Context
	ListenPort int
	Logger     *slog.Logger
	Wg         *sync.WaitGroup
}
