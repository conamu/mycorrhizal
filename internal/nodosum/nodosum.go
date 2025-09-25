package nodosum

import (
	"context"
	"crypto/tls"
	"errors"
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
	nodeId           string
	ctx              context.Context
	listener         net.Listener
	logger           *slog.Logger
	nodeRegistry     *sync.Map
	channelRegistry  *sync.Map
	wg               *sync.WaitGroup
	handshakeTimeout time.Duration
	tlsEnabled       bool
	tlsConfig        *tls.Config
}

func New(cfg *Config) (*Nodosum, error) {
	var tlsConf *tls.Config

	lAddr := &net.TCPAddr{Port: cfg.ListenPort}
	addrString := lAddr.String()

	if cfg.TlsEnabled {
		cfg.Logger.Debug("running with TLS enabled")
		tlsConf = &tls.Config{
			ServerName:   "localhost",
			RootCAs:      cfg.TlsCACert,
			Certificates: []tls.Certificate{*cfg.TlsCert},
		}
	}
	listener, err := net.Listen("tcp", addrString)
	if err != nil {
		return nil, err
	}

	return &Nodosum{
		nodeId:           cfg.NodeId,
		ctx:              cfg.Ctx,
		listener:         listener,
		logger:           cfg.Logger,
		nodeRegistry:     &sync.Map{},
		channelRegistry:  &sync.Map{},
		wg:               cfg.Wg,
		handshakeTimeout: cfg.HandshakeTimeout,
		tlsEnabled:       cfg.TlsEnabled,
		tlsConfig:        tlsConf,
	}, nil
}

func (n *Nodosum) Start() {
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
	n.channelRegistry.Range(func(k, v interface{}) bool {
		id := k.(string)
		n.closeConnChannel(id)
		return true
	})
}

func (n *Nodosum) listen() error {
	n.wg.Go(
		func() {
			<-n.ctx.Done()
			err := n.listener.Close()
			if err != nil {
				n.logger.Info("listener close failed", "error", err.Error())
			}
			n.logger.Info("listener closed")
		},
	)

	// Listener Accept Loop
	for {
		select {
		case <-n.ctx.Done():
			return nil
		default:
			conn, err := n.listener.Accept()
			if err != nil {
				if !errors.Is(err, net.ErrClosed) {
					n.logger.Error("error accepting TCP connection", err.Error())
				}
				continue
			}
			if n.tlsEnabled {
				tlsConn := tls.Server(conn, n.tlsConfig)
				hsCtx, _ := context.WithDeadline(n.ctx, time.Now().Add(n.handshakeTimeout))
				err = tlsConn.HandshakeContext(hsCtx)
				if err != nil {
					n.logger.Warn("error setting handshake context", "error", err.Error())
				}
				err = tlsConn.Handshake()
				if err != nil {
					n.logger.Error("error handshake TLS connection", "error", err.Error(), "remote", conn.RemoteAddr())
					conn.Close()
					return nil
				}
				conn = tlsConn
			}
			n.wg.Add(1)
			go n.handleConn(conn)
		}
	}
}
