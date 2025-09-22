package mycorrizal

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Mycorrizal interface {
	Start() error
}

type mycorrizal struct {
	nodeId        string
	ctx           context.Context
	logger        *slog.Logger
	httpClient    *http.Client
	connReg       *connectionRegistry
	discoveryMode int
	nodeAddrs     []net.TCPAddr
	singleMode    bool
}

func New(cfg *Config) (Mycorrizal, error) {
	ctx := cfg.Ctx

	id := os.Getenv("MYCORRIZAL_ID")

	if id == "" {
		// Use the IDs of env variable to enable
		// having the same IDs as the containers in the
		// Orchestrator for better visibility or generate own IDs
		id = uuid.NewString()
	}

	var httpClient *http.Client
	if cfg.DiscoveryMode == DC_MODE_CONSUL {
		var tlsConfig *tls.Config
		if cfg.HttpClientTLSEnabled {
			if cfg.HttpClientTLSCACert == nil || cfg.HttpClientTLSCert == nil {
				return nil, errors.New("enabling TLS requires setting HttpClientTLSCaCert and HttpClientTLSCert")
			}

			tlsConfig = &tls.Config{
				RootCAs:      cfg.HttpClientTLSCACert,
				Certificates: []tls.Certificate{*cfg.HttpClientTLSCert},
			}
		}

		httpClient = &http.Client{
			Transport: &http.Transport{
				MaxIdleConnsPerHost: 1,
				MaxIdleConns:        1,
				IdleConnTimeout:     5 * time.Second,
				TLSClientConfig:     tlsConfig,
			},
		}

	}

	if cfg.DiscoveryMode == DC_MODE_STATIC && cfg.NodeAddrs == nil {
		return nil, errors.New("static discovery mode reuires NodeAddrs to be set")
	}

	if cfg.DiscoveryMode == DC_MODE_STATIC && len(cfg.NodeAddrs) == 0 {
		cfg.Logger.Warn("running in static discovery mode but found no addresses in NodeAddrs array")
	}

	if cfg.DiscoveryMode == DC_MODE_CONSUL || cfg.DiscoveryMode == DC_MODE_DNS_SD && cfg.DiscoveryHost == nil {
		return nil, errors.New("discovery modes consul and DNS Service discovery need discoveryHost to be set")
	}

	if cfg.SingleMode {
		cfg.Logger.Info("Node running in single mode, no Cluster connections")
	}

	return &mycorrizal{
		nodeId:        id,
		ctx:           ctx,
		logger:        cfg.Logger,
		httpClient:    httpClient,
		connReg:       newConnectionRegistry(),
		discoveryMode: cfg.DiscoveryMode,
		nodeAddrs:     cfg.NodeAddrs,
		singleMode:    cfg.SingleMode,
	}, nil
}

func (mc *mycorrizal) Start() error {
	mc.logger.Info("mycorrizal started")
	return nil
}

// connectionRegistry is needed to keep track of connections and merge connections for efficiency
type connectionRegistry struct {
	mu       sync.Mutex
	outbound map[string]*net.TCPConn
	inbound  map[string]*net.TCPConn
}

func newConnectionRegistry() *connectionRegistry {
	return &connectionRegistry{
		mu:       sync.Mutex{},
		outbound: make(map[string]*net.TCPConn),
		inbound:  make(map[string]*net.TCPConn),
	}
}
