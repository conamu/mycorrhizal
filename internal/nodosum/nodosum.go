package nodosum

import (
	"context"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log/slog"
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
- Establish Connections in Star Network Topology (all nodes have a connection to all nodes) X
- Manage connections and keep them up X
- Provide communication interface to abstract away the cluster X
  (this should feel like one big App, even though it could be spread on 10 nodes/instances)
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
	delegate               *Delegate
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

func New(cfg *Config) (*Nodosum, error) {
	onePassClient, err := onepassword.NewClient(cfg.Ctx,
		onepassword.WithServiceAccountToken(cfg.OnePasswordToken),
		onepassword.WithIntegrationInfo("Mycorrizal auto Cert", "v1.0.0"),
	)
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
		Versions:        []quic.Version{quic.Version2},
		MaxIdleTimeout:  time.Minute * 2,
		KeepAlivePeriod: time.Second * 10,
	}

	n := &Nodosum{
		nodeId:                 cfg.NodeId,
		nodeMeta:               cfg.NodeAddrs,
		ctx:                    ctx,
		cancel:                 cancel,
		onePasswordClient:      onePassClient,
		quicPort:               cfg.QuicPort,
		quicTransport:          quicTransport,
		quicConfig:             quicConf,
		quicConns:              &quicConns{conns: make(map[string]*quic.Conn)},
		quicApplicationStreams: &quicApplicationStreams{streams: make(map[string]*quic.Stream)},
		udpConn:                udpConn,
		sharedSecret:           cfg.SharedSecret,
		logger:                 cfg.Logger,
		applications:           &applications{applications: make(map[string]*application)},
		wg:                     cfg.Wg,
		tlsCaCert:              cfg.TlsCACert,
		tlsCaKey:               cfg.TlsCAKey,
		readyChan:              make(chan any),
	}

	delegate := &Delegate{
		Nodosum: n,
		dta: &delegateDialAttempts{
			att: map[string]int{},
		},
	}

	cfg.MemberlistConfig.Events = delegate
	cfg.MemberlistConfig.Delegate = delegate
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

	n.delegate = delegate
	n.ml = ml

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
		ClientCAs:    ca, // For verifying client certificates
		Certificates: []tls.Certificate{*nodeCert},
		ClientAuth:   tls.RequireAndVerifyClientCert, // Require mutual TLS
		NextProtos:   []string{"mycorrizal"},
	}

	// For Security, unset any onePassword related things
	n.onePasswordClient = nil
	cfg.OnePasswordToken = ""

	return n, nil
}

// createDialTLSConfig creates a TLS config for dialing a specific remote node.
// It clones the base TLS config and sets the ServerName to the remote node's ID
// to ensure proper certificate verification during the TLS handshake.
func (n *Nodosum) createDialTLSConfig(remoteNodeID string) *tls.Config {
	// Clone the base config to avoid modifying the shared instance
	dialConfig := n.tlsConfig.Clone()

	// Set ServerName to the remote node's ID for proper certificate verification
	dialConfig.ServerName = remoteNodeID

	return dialConfig
}

func (n *Nodosum) Start() {
	defer func() {
		if r := recover(); r != nil {
			n.logger.Error("panic in Start() recovered", "panic", r)
			n.cancel()
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

		// Establish QUIC connections to initial nodes
		// (NotifyJoin is only called for nodes that join AFTER startup)
		n.logger.Debug("Start: establishing QUIC connections to initial nodes")
		for _, member := range n.ml.Members() {
			if member.Name != n.nodeId {
				n.logger.Debug("Start: connecting to initial node", "node", member.Name)
				// Manually trigger QUIC connection establishment
				go n.delegate.NotifyJoin(member)
			}
		}

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
	err = n.udpConn.Close()
	if err != nil {
		n.logger.Error("failed to close UDP connection", "err", err)
	}
	n.logger.Debug("nodosum shutdown waiting on routines to exit...")
	n.wg.Wait()
}

func (n *Nodosum) Id() string {
	return n.nodeId
}
