package nodosum

import (
	"context"
	"log/slog"
	"sync"
)

type Config struct {
	NodeId     string
	Ctx        context.Context
	ListenPort int
	Logger     *slog.Logger
	Wg         *sync.WaitGroup
}
