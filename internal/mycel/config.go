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

	// GeoFlushInterval controls how often the geo flush worker runs.
	// Defaults to 30 seconds if zero.
	GeoFlushInterval time.Duration

	// GeoFlushFunc is called by the flush worker on every interval.
	// If nil, flushing is disabled and no Postgres persistence occurs.
	GeoFlushFunc GeoFlushFunc
}
