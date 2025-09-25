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
	nodeId           string
	ctx              context.Context
	listener         net.Listener
	logger           *slog.Logger
	connections      *sync.Map
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
		connections:      &sync.Map{},
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
	n.connections.Range(func(k, v interface{}) bool {
		id := k.(string)
		n.closeConnChannel(id)
		return true
	})
}

// Broadcast synchronously sends a command to all nodes and returns all answered commands
func (n *Nodosum) Broadcast(c Command) []Command {
	answeredCommands := []Command{}

	n.connections.Range(func(k, v interface{}) bool {
		id := k.(string)
		connChan := v.(*nodeConn)
		n.logger.Debug("broadcasting to node connection", "id", id)
		err := c.packAndSend(connChan.writeChan)
		if err != nil {
			n.logger.Error("broadcast error", "error", err.Error())
		}
		err = c.receiveAndUnpack(connChan.readChan)
		if err != nil {
			n.logger.Error("broadcast receive error", "error", err.Error())
		}
		answeredCommands = append(answeredCommands, c)
		return true
	})
	return nil
}

// Send sends a command to one node by id and returns the answered command
func (n *Nodosum) Send(id string, c Command) Command {
	n.logger.Debug("sending to node connection", "id", id)

	val, ok := n.connections.Load(id)
	if !ok {
		return nil
	}
	connChann := val.(*nodeConn)
	err := c.packAndSend(connChann.writeChan)
	if err != nil {
		n.logger.Error("send error", "error", err.Error())
	}
	err = c.receiveAndUnpack(connChann.readChan)
	if err != nil {
		n.logger.Error("send receive error", "error", err.Error())
	}
	return c
}
