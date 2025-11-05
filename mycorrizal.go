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

	"github.com/conamu/mycorrizal/internal/nodosum"
	"github.com/google/uuid"
)

type Mycorrizal interface {
	Start() error
	Shutdown() error
	RegisterApplication(uniqueIdentifier string) nodosum.Application
	GetApplication(uniqueIdentifier string) nodosum.Application
}

type mycorrizal struct {
	nodeId        string
	ctx           context.Context
	wg            *sync.WaitGroup
	cancel        context.CancelFunc
	logger        *slog.Logger
	httpClient    *http.Client
	discoveryMode int
	nodeAddrs     []net.TCPAddr
	singleMode    bool
	nodosum       *nodosum.Nodosum
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

	if (cfg.DiscoveryMode == DC_MODE_CONSUL || cfg.DiscoveryMode == DC_MODE_DNS_SD) && cfg.DiscoveryHost == nil {
		return nil, errors.New("discovery modes consul and DNS Service discovery need discoveryHost to be set")
	}

	if cfg.SingleMode {
		cfg.Logger.Info("Node running in single mode, no Cluster connections")
	}

	ctx, cancel := context.WithCancel(ctx)
	wg := &sync.WaitGroup{}

	nodeMeta := nodosum.NodeMetaMap{
		Mu:  sync.Mutex{},
		IPs: make([]string, 0),
		Map: make(map[string]nodosum.NodeMeta),
	}

	for _, addr := range cfg.NodeAddrs {
		nodeMeta.IPs = append(nodeMeta.IPs, addr.String())
	}

	nodosumConfig := &nodosum.Config{
		NodeId:                 id,
		NodeAddrs:              &nodeMeta,
		Ctx:                    ctx,
		ListenPort:             cfg.ListenPort,
		Logger:                 cfg.Logger,
		Wg:                     wg,
		HandshakeTimeout:       cfg.HandshakeTimeout,
		SharedSecret:           cfg.SharedSecret,
		TlsEnabled:             cfg.ClusterTLSEnabled,
		TlsHostName:            cfg.ClusterTLSHostName,
		TlsCACert:              cfg.ClusterTLSCACert,
		TlsCert:                cfg.ClusterTLSCert,
		MultiplexerBufferSize:  cfg.MultiplexerBufferSize,
		MultiplexerWorkerCount: cfg.MultiplexerWorkerCount,
	}

	ndsm, err := nodosum.New(nodosumConfig)
	if err != nil {
		cancel()
		return nil, err
	}

	return &mycorrizal{
		nodeId:        id,
		ctx:           ctx,
		wg:            wg,
		cancel:        cancel,
		logger:        cfg.Logger,
		httpClient:    httpClient,
		discoveryMode: cfg.DiscoveryMode,
		nodeAddrs:     cfg.NodeAddrs,
		singleMode:    cfg.SingleMode,
		nodosum:       ndsm,
	}, nil
}

func (mc *mycorrizal) Start() error {
	mc.logger.Info("mycorrizal starting")
	err := mc.nodosum.Start()
	if err != nil {
		mc.logger.Error(err.Error())
		mc.cancel()
		return err
	}
	mc.logger.Info("mycorrizal startup complete")
	return nil
}

func (mc *mycorrizal) Shutdown() error {
	mc.logger.Info("mycorrizal shutting down")
	mc.nodosum.Shutdown()
	mc.cancel()
	mc.logger.Debug("mycorrizal shutting down waiting on goroutines...")
	mc.wg.Wait()
	mc.logger.Info("mycorrizal shutdown complete")
	return nil
}

func (mc *mycorrizal) RegisterApplication(uniqueIdentifier string) nodosum.Application {
	return mc.nodosum.RegisterApplication(uniqueIdentifier)
}

func (mc *mycorrizal) GetApplication(uniqueIdentifier string) nodosum.Application {
	return mc.nodosum.GetApplication(uniqueIdentifier)
}
