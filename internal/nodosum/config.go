package nodosum

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"log/slog"
	"sync"
	"time"

	"github.com/hashicorp/memberlist"
	"go.opentelemetry.io/otel/metric"
)

type Config struct {
	NodeId            string
	NodeAddrs         *NodeMetaMap
	Ctx               context.Context
	ListenPort        int
	SharedSecret      string
	HandshakeTimeout  time.Duration
	Logger            *slog.Logger
	Meter             metric.Meter
	Wg                *sync.WaitGroup
	CACert            *x509.Certificate
	CAKey             *rsa.PrivateKey
	MemberlistConfig  *memberlist.Config
	QuicListenPort    int
	QuicAdvertisePort int
}
