package mycel

import (
	"context"
	"log/slog"
	"time"

	"github.com/conamu/mycorrizal/internal/nodosum"
	"go.opentelemetry.io/otel/metric"
)

type Config struct {
	Ctx                context.Context
	Logger             *slog.Logger
	Meter              metric.Meter
	Nodosum            *nodosum.Nodosum
	Replicas           int
	RemoteTimeout      time.Duration
	RebalancerInterval time.Duration // How often the rebalancer runs. Defaults to 30s if zero.
}
