package nodosum

import (
	"context"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"log/slog"
	"sync"
	"time"

	"github.com/hashicorp/memberlist"
)

type Config struct {
	NodeId                 string
	NodeAddrs              *NodeMetaMap
	Ctx                    context.Context
	ListenPort             int
	SharedSecret           string
	HandshakeTimeout       time.Duration
	Logger                 *slog.Logger
	Wg                     *sync.WaitGroup
	TlsEnabled             bool
	TlsHostName            string
	TlsCACert              *x509.Certificate
	TlsCAKey               *rsa.PrivateKey
	TlsCert                *tls.Certificate
	OnePasswordToken       string
	MultiplexerBufferSize  int
	MultiplexerWorkerCount int
	MemberlistConfig       *memberlist.Config
	QuicPort               int
}
