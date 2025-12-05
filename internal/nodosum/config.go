package nodosum

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"log/slog"
	"sync"
	"time"

	"github.com/hashicorp/memberlist"
)

type Config struct {
	NodeId           string
	NodeAddrs        *NodeMetaMap
	Ctx              context.Context
	ListenPort       int
	SharedSecret     string
	HandshakeTimeout time.Duration
	Logger           *slog.Logger
	Wg               *sync.WaitGroup
	TlsHostName      string
	TlsCACert        *x509.Certificate
	TlsCAKey         *rsa.PrivateKey
	OnePasswordToken string
	MemberlistConfig *memberlist.Config
	QuicPort         int
}
