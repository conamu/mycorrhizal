package nodosum

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net"
	"sync"
	"time"
)

/*

SCOPE

- Discover Instances via Consul API/DNS-SD
- Establish Connections in Star Network Topology (all nodes have a connection to all nodes)
- Manage connections and keep them up X
- Provide communication interface to abstract away the cluster
  (this should feel like one big App, even though it could be spread on 10 nodes/instances)
- Provide Interface to create, read, update and delete cluster store resources
  and find/set their location on the cluster.
- Authenticate and Encrypt all Intra-Cluster Communication X

*/

type Nodosum struct {
	nodeId       string
	ctx          context.Context
	listener     net.Listener
	logger       *slog.Logger
	connections  *sync.Map
	applications *sync.Map
	// globalReadChannel transfers all incoming packets from connections to the multiplexer
	globalReadChannel chan any
	// globalWriteChannel transfers all outgoing packets from applications to the multiplexer
	globalWriteChannel    chan any
	wg                    *sync.WaitGroup
	handshakeTimeout      time.Duration
	tlsEnabled            bool
	tlsConfig             *tls.Config
	multiplexerBufferSize int
	muxWorkerCount        int
}

func New(cfg *Config) (*Nodosum, error) {
	var tlsConf *tls.Config

	lAddr := &net.TCPAddr{Port: cfg.ListenPort}
	addrString := lAddr.String()

	if cfg.TlsEnabled {
		cfg.Logger.Debug("running with TLS enabled")
		tlsConf = &tls.Config{
			ServerName:   cfg.TlsHostName,
			RootCAs:      cfg.TlsCACert,
			Certificates: []tls.Certificate{*cfg.TlsCert},
		}
	}
	listener, err := net.Listen("tcp", addrString)
	if err != nil {
		return nil, err
	}

	return &Nodosum{
		nodeId:                cfg.NodeId,
		ctx:                   cfg.Ctx,
		listener:              listener,
		logger:                cfg.Logger,
		connections:           &sync.Map{},
		applications:          &sync.Map{},
		globalReadChannel:     make(chan any, cfg.MultiplexerBufferSize),
		globalWriteChannel:    make(chan any, cfg.MultiplexerBufferSize),
		wg:                    cfg.Wg,
		handshakeTimeout:      cfg.HandshakeTimeout,
		tlsEnabled:            cfg.TlsEnabled,
		tlsConfig:             tlsConf,
		multiplexerBufferSize: cfg.MultiplexerBufferSize,
		muxWorkerCount:        cfg.MultiplexerWorkerCount,
	}, nil
}

func (n *Nodosum) Start() {

	n.StartMultiplexer()

	n.wg.Go(
		func() {
			err := n.listen()
			if err != nil {
				n.logger.Error("nodosum error while starting", "error", err.Error())
			}
		},
	)
}

func (n *Nodosum) Shutdown() {
	n.connections.Range(func(k, v interface{}) bool {
		id := k.(uint32)
		n.closeConnChannel(id)
		return true
	})
}
