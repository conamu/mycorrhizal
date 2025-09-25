package nodosum

import (
	"context"
	"crypto/tls"
	"crypto/x509"
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
	TlsEnabled       bool
	TlsCACert        *x509.CertPool
	TlsCert          *tls.Certificate
}
