package nodosum

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

type Config struct {
	NodeId           string
	Ctx              context.Context
	ListenPort       int
	HandshakeTimeout time.Duration
	Logger           *slog.Logger
	Wg               *sync.WaitGroup
}
