package nodosum

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"math/rand"
	"net"
	"sync"
	"time"

	"github.com/conamu/go-worker"
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
	nodeId            string
	nodeMeta          *NodeMetaMap
	ctx               context.Context
	connInit          *connInit
	listenerTcp       net.Listener
	udpConn           *net.UDPConn
	sharedSecret      string
	logger            *slog.Logger
	connections       *sync.Map
	applications      *sync.Map
	nodeAppSyncWorker *worker.Worker
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

type connInit struct {
	mu     sync.Mutex
	val    uint32
	tokens map[string]string
}

func New(cfg *Config) (*Nodosum, error) {
	var tlsConf *tls.Config

	tcpLocalAddr := &net.TCPAddr{Port: cfg.ListenPort}
	addrString := tcpLocalAddr.String()

	if cfg.TlsEnabled {
		cfg.Logger.Debug("running with TLS enabled")
		tlsConf = &tls.Config{
			ServerName:   cfg.TlsHostName,
			RootCAs:      cfg.TlsCACert,
			Certificates: []tls.Certificate{*cfg.TlsCert},
		}
	}
	listenerTcp, err := net.Listen("tcp", addrString)
	if err != nil {
		return nil, err
	}

	udpLocalAddr := &net.UDPAddr{Port: cfg.ListenPort}
	udpConn, err := net.ListenUDP("udp", udpLocalAddr)

	n := &Nodosum{
		nodeId:   cfg.NodeId,
		nodeMeta: cfg.NodeAddrs,
		ctx:      cfg.Ctx,
		connInit: &connInit{
			mu:     sync.Mutex{},
			val:    rand.Uint32(),
			tokens: make(map[string]string),
		},
		listenerTcp:           listenerTcp,
		udpConn:               udpConn,
		sharedSecret:          cfg.SharedSecret,
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
	}

	nodeAppSync := worker.NewWorker(cfg.Ctx, "node-app-sync", cfg.Wg, n.nodeAppSyncTask, cfg.Logger, time.Second*1)
	n.nodeAppSyncWorker = nodeAppSync

	return n, nil
}

func (n *Nodosum) Start() error {

	x := len([]byte(n.sharedSecret))

	if x != 64 {
		return errors.New("invalid shared secret, must be 64 bytes")
	}

	n.StartMultiplexer()

	n.wg.Go(
		func() {
			n.listenTcp()
		},
	)
	n.wg.Go(
		func() {
			n.listenUdp()
		},
	)

	go n.nodeAppSyncWorker.Start()

	packet := n.encodeHandshakePacket(&handshakeUdpPacket{
		Version:  1,
		Type:     HELLO,
		ConnInit: n.connInit.val,
		Id:       n.nodeId,
		Secret:   n.sharedSecret,
	})

	n.nodeMeta.Mu.Lock()
	for _, ip := range n.nodeMeta.IPs {

		a, err := net.ResolveUDPAddr("udp", ip)
		if err != nil {
			n.logger.Error("failed to resolve udp addr", "err", err)
			continue
		}

		_, err = n.udpConn.WriteToUDP(packet, a)
		if err != nil {
			n.logger.Error("failed to write udp packet", "err", err)
		}
	}
	n.nodeMeta.Mu.Unlock()

	return nil
}

func (n *Nodosum) Shutdown() {
	n.nodeAppSyncWorker.Stop()
	n.connections.Range(func(k, v interface{}) bool {
		id := k.(string)
		n.closeConnChannel(id)
		return true
	})
}
