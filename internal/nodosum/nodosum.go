package nodosum

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/1password/onepassword-sdk-go"
	"github.com/conamu/go-worker"
	"github.com/hashicorp/memberlist"
	"github.com/quic-go/quic-go"
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
	cancel            context.CancelFunc
	onePasswordClient *onepassword.Client
	ml                *memberlist.Memberlist
	connInit          *connInit
	listenerTcp       net.Listener
	quicPort          int
	quicTransport     *quic.Transport
	quicConfig        *quic.Config
	quicConns         *quicConns
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
	readyChan             chan any
}

type quicConns struct {
	sync.RWMutex
	conns map[string]*quic.Conn
}
type connInit struct {
	mu     sync.Mutex
	val    uint32
	tokens map[string]string
}

func New(cfg *Config) (*Nodosum, error) {
	var tlsConf *tls.Config

	cfg.MemberlistConfig.AdvertiseAddr = "127.0.0.1"
	cfg.MemberlistConfig.BindAddr = "127.0.0.1"
	cfg.MemberlistConfig.LogOutput = os.Stdout
	cfg.MemberlistConfig.SecretKey = []byte(cfg.SharedSecret)
	cfg.MemberlistConfig.Name = cfg.NodeId

	ml, err := memberlist.Create(cfg.MemberlistConfig)
	if err != nil {
		return nil, err
	}

	onePassClient, err := onepassword.NewClient(cfg.Ctx,
		onepassword.WithServiceAccountToken(cfg.OnePasswordToken),
		onepassword.WithIntegrationInfo("Mycorrizal auto Cert", "v1.0.0"),
	)
	if err != nil {
		return nil, err
	}

	tcpLocalAddr := &net.TCPAddr{Port: cfg.ListenPort}
	addrString := tcpLocalAddr.String()

	listenerTcp, err := net.Listen("tcp", addrString)
	if err != nil {
		return nil, err
	}

	udpLocalAddr := &net.UDPAddr{Port: cfg.ListenPort}
	udpConn, err := net.ListenUDP("udp", udpLocalAddr)

	localQuicAddr, err := net.ListenUDP("udp", &net.UDPAddr{Port: cfg.QuicPort})
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(cfg.Ctx)

	quicTransport := &quic.Transport{
		Conn:                             localQuicAddr,
		DisableVersionNegotiationPackets: true,
	}

	quicConf := &quic.Config{
		Versions: []quic.Version{quic.Version2},
	}

	n := &Nodosum{
		nodeId:   cfg.NodeId,
		nodeMeta: cfg.NodeAddrs,
		ctx:      ctx,
		cancel:   cancel,
		ml:       ml,
		connInit: &connInit{
			mu:     sync.Mutex{},
			val:    rand.Uint32(),
			tokens: make(map[string]string),
		},
		onePasswordClient:     onePassClient,
		quicPort:              cfg.QuicPort,
		quicTransport:         quicTransport,
		quicConfig:            quicConf,
		quicConns:             &quicConns{conns: make(map[string]*quic.Conn)},
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
		readyChan:             make(chan any),
	}

	cfg.MemberlistConfig.Events = &Delegate{
		Nodosum: n,
	}

	nodeCert, caCert, err := n.generateNodeCert()
	if err != nil {
		return nil, err
	}

	if cfg.TlsEnabled {
		ca := x509.NewCertPool()
		ca.AddCert(caCert)
		cfg.Logger.Debug("running with TLS enabled")
		tlsConf = &tls.Config{
			ServerName:   cfg.NodeId,
			RootCAs:      ca,
			Certificates: []tls.Certificate{*nodeCert},
			NextProtos:   []string{"mycorrizal"},
		}
	}

	nodeAppSync := worker.NewWorker(cfg.Ctx, "node-app-sync", cfg.Wg, n.nodeAppSyncTask, cfg.Logger, time.Second*3)
	n.nodeAppSyncWorker = nodeAppSync

	return n, nil
}

func (n *Nodosum) Start() error {

	n.startMultiplexer()

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
	n.wg.Go(
		func() {
			n.listenQuic()
		})

	n.nodeMeta.Lock()
	filteredNodes := n.filterNodes(n.nodeMeta.IPs)
	n.nodeMeta.Unlock()

	nodesConnected, err := n.ml.Join(filteredNodes)
	if err != nil {
		n.logger.Error("joining initial seed nodes failed", err)
	}
	n.logger.Debug(fmt.Sprintf("%d nodes joined", nodesConnected))

	go n.nodeAppSyncWorker.Start()

	close(n.readyChan)

	return nil
}

func (n *Nodosum) filterNodes(ips []string) []string {
	filtered := []string{}
	for _, ip := range ips {
		localAddr := n.ml.LocalNode().Addr.String() + ":" + strconv.Itoa(int(n.ml.LocalNode().Port))
		if ip != localAddr {
			filtered = append(filtered, ip)
		}
	}
	return filtered
}

func (n *Nodosum) Ready(timeout time.Duration) error {
	t := time.NewTimer(timeout)
	for {
		select {
		case <-n.readyChan:
			n.logger.Debug("nodosum ready")
			return nil
		case <-t.C:
			return errors.New("nodosum did not send ready signal before timeout")
		case <-n.ctx.Done():
			return errors.New("context closed")
		}
	}
}

func (n *Nodosum) Shutdown() {
	err := n.ml.Leave(30 * time.Second)
	if err != nil {
		n.logger.Error("ml failed to leave properly", "err", err)
	}
	n.nodeAppSyncWorker.Stop()
	n.connections.Range(func(k, v interface{}) bool {
		id := k.(string)
		n.closeConnChannel(id)
		return true
	})
	n.cancel()
	n.logger.Debug("nodosum shutdown waiting on routines to exit...")
	n.wg.Wait()
}

func (n *Nodosum) Id() string {
	return n.nodeId
}
