package nodosum

import (
	"context"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log/slog"
	"math/rand"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/1password/onepassword-sdk-go"
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
	nodeId                 string
	nodeMeta               *NodeMetaMap
	ctx                    context.Context
	cancel                 context.CancelFunc
	startOnce              sync.Once
	onePasswordClient      *onepassword.Client
	ml                     *memberlist.Memberlist
	connInit               *connInit
	listenerTcp            net.Listener
	quicPort               int
	quicTransport          *quic.Transport
	quicConfig             *quic.Config
	quicConns              *quicConns
	quicApplicationStreams *quicApplicationStreams
	udpConn                *net.UDPConn
	sharedSecret           string
	logger                 *slog.Logger
	applications           *applications
	wg                     *sync.WaitGroup
	tlsConfig              *tls.Config
	tlsCaCert              *x509.Certificate
	tlsCaKey               *rsa.PrivateKey
	readyChan              chan any
}

type quicConns struct {
	sync.RWMutex
	conns map[string]*quic.Conn
}

type applications struct {
	sync.RWMutex
	applications map[string]*application
}

type quicApplicationStreams struct {
	sync.RWMutex
	streams map[string]*quic.Stream
}

type connInit struct {
	mu     sync.Mutex
	val    uint32
	tokens map[string]string
}

func New(cfg *Config) (*Nodosum, error) {
	cfg.MemberlistConfig.AdvertiseAddr = "127.0.0.1"
	cfg.MemberlistConfig.BindAddr = "127.0.0.1"
	cfg.MemberlistConfig.LogOutput = os.Stdout
	cfg.MemberlistConfig.SecretKey = []byte(cfg.SharedSecret)
	cfg.MemberlistConfig.Name = cfg.NodeId
	cfg.MemberlistConfig.TCPTimeout = time.Second * 3

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
		onePasswordClient:      onePassClient,
		quicPort:               cfg.QuicPort,
		quicTransport:          quicTransport,
		quicConfig:             quicConf,
		quicConns:              &quicConns{conns: make(map[string]*quic.Conn)},
		quicApplicationStreams: &quicApplicationStreams{streams: make(map[string]*quic.Stream)},
		listenerTcp:            listenerTcp,
		udpConn:                udpConn,
		sharedSecret:           cfg.SharedSecret,
		logger:                 cfg.Logger,
		applications:           &applications{applications: make(map[string]*application)},
		wg:                     cfg.Wg,
		tlsCaCert:              cfg.TlsCACert,
		tlsCaKey:               cfg.TlsCAKey,
		readyChan:              make(chan any),
	}

	cfg.MemberlistConfig.Events = &Delegate{
		Nodosum: n,
	}

	nodeCert, caCert, err := n.generateNodeCert()
	if err != nil {
		return nil, err
	}

	// Memberlist and Quic will only work with TLS. This library enforces the user to use TLS.
	ca := x509.NewCertPool()
	ca.AddCert(caCert)
	n.tlsConfig = &tls.Config{
		ServerName:   cfg.NodeId,
		RootCAs:      ca,
		Certificates: []tls.Certificate{*nodeCert},
		NextProtos:   []string{"mycorrizal"},
	}

	// For Security, unset any onePassword related things
	n.onePasswordClient = nil
	cfg.OnePasswordToken = ""

	return n, nil
}

func (n *Nodosum) Start() {
	defer func() {
		if r := recover(); r != nil {
			n.logger.Error("panic in Start() recovered", "panic", r)
			close(n.readyChan) // Still signal (though with error state)
			panic(r)           // Re-panic after cleanup
		}
	}()

	n.startOnce.Do(func() {
		n.logger.Debug("Start: launching QUIC listener")
		n.wg.Go(func() {
			n.listenQuic()
		})

		n.logger.Debug("Start: filtering nodes")
		n.nodeMeta.Lock()
		filteredNodes := n.filterNodes(n.nodeMeta.IPs)
		n.nodeMeta.Unlock()
		n.logger.Debug("Start: filtered nodes", "count", len(filteredNodes), "nodes", filteredNodes)

		n.logger.Debug("Start: calling memberlist.Join()")
		nodesConnected, err := n.ml.Join(filteredNodes)
		if err != nil {
			n.logger.Error("joining initial seed nodes failed", err)
		}
		n.logger.Debug("Start: Join() completed", "nodesConnected", nodesConnected)

		n.logger.Debug("Start: closing readyChan")
		close(n.readyChan)
		n.logger.Debug("Start: completed successfully")
	})
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
	n.cancel()
	n.logger.Debug("nodosum shutdown waiting on routines to exit...")
	n.wg.Wait()
}

func (n *Nodosum) Id() string {
	return n.nodeId
}
