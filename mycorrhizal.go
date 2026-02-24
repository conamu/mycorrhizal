package mycorrhizal

import (
	"context"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/conamu/mycorrhizal/internal/mycel"
	"github.com/conamu/mycorrhizal/internal/nodosum"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"

	_ "net/http/pprof"
)

type Mycorrhizal interface {
	Start() error
	Ready(timeout time.Duration) error
	Shutdown() error
	RegisterApplication(uniqueIdentifier string) nodosum.Application
	GetApplication(uniqueIdentifier string) nodosum.Application
	Cache() mycel.Cache
}

type mycorrhizal struct {
	nodeId          string
	ctx             context.Context
	wg              *sync.WaitGroup
	cancel          context.CancelFunc
	logger          *slog.Logger
	meter           metric.Meter
	httpClient      *http.Client
	discoveryMode   int
	nodeAddrs       []net.TCPAddr
	singleMode      bool
	nodosum         *nodosum.Nodosum
	mycel           mycel.Mycel
	debug           bool
	debugHttpServer *http.Server
	readyChan       chan any
}

func New(cfg *Config) (Mycorrhizal, error) {
	ctx := cfg.Ctx

	id := os.Getenv("MYCORRHIZAL_ID")

	if id == "" {
		// Use the IDs of env variable to enable
		// having the same IDs as the containers in the
		// Orchestrator for better visibility or generate own IDs
		id = uuid.NewString()
	}

	if len(cfg.CaCertPEM) == 0 {
		return nil, errors.New("missing CA certificate. Provide a PEM-encoded intermediary CA certificate and key for the cluster to secure itself with automatically generated client certificates")
	}
	if len(cfg.CaKeyPEM) == 0 {
		return nil, errors.New("missing CA key. Provide a PEM-encoded intermediary CA certificate and key for the cluster to secure itself with automatically generated client certificates")
	}

	certBlock, _ := pem.Decode(cfg.CaCertPEM)
	if certBlock == nil {
		return nil, errors.New("CaCertPEM: failed to decode PEM block")
	}
	caCert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("CaCertPEM: %w", err)
	}

	keyBlock, _ := pem.Decode(cfg.CaKeyPEM)
	if keyBlock == nil {
		return nil, errors.New("CaKeyPEM: failed to decode PEM block")
	}
	var caKey *rsa.PrivateKey
	switch keyBlock.Type {
	case "RSA PRIVATE KEY":
		caKey, err = x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
		if err != nil {
			return nil, fmt.Errorf("CaKeyPEM (PKCS1): %w", err)
		}
	case "PRIVATE KEY":
		parsed, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
		if err != nil {
			return nil, fmt.Errorf("CaKeyPEM (PKCS8): %w", err)
		}
		var ok bool
		caKey, ok = parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("CaKeyPEM: only RSA private keys are supported")
		}
	default:
		return nil, fmt.Errorf("CaKeyPEM: unsupported PEM block type %q", keyBlock.Type)
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
		return nil, errors.New("static discovery mode requires NodeAddrs to be set")
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

	nodeMeta := nodosum.NodeMetaMap{
		IPs: make([]string, 0),
		Map: make(map[string]nodosum.NodeMeta),
	}

	for _, addr := range cfg.NodeAddrs {
		nodeMeta.IPs = append(nodeMeta.IPs, addr.String())
	}

	meter := otel.GetMeterProvider().Meter("mycorrhizal")

	nodosumConfig := &nodosum.Config{
		NodeId:            id,
		NodeAddrs:         &nodeMeta,
		Ctx:               ctx,
		ListenPort:        cfg.ListenPort,
		Logger:            cfg.Logger,
		Meter:             meter,
		Wg:                &sync.WaitGroup{},
		HandshakeTimeout:  cfg.HandshakeTimeout,
		SharedSecret:      cfg.SharedSecret,
		CACert:            caCert,
		CAKey:             caKey,
		MemberlistConfig:  cfg.MemberlistConfig,
		QuicListenPort:    cfg.QuicListenPort,
		QuicAdvertisePort: cfg.QuicAdvertisePort,
	}

	ndsm, err := nodosum.New(nodosumConfig)
	if err != nil {
		cancel()
		return nil, err
	}

	mycelConfig := &mycel.Config{
		Ctx:           ctx,
		Logger:        cfg.Logger,
		Meter:         meter,
		Nodosum:       ndsm,
		Replicas:      cfg.CacheReplicaCount,
		RemoteTimeout: cfg.HandshakeTimeout,
	}

	mcl, err := mycel.New(mycelConfig)
	if err != nil {
		cancel()
		return nil, err
	}

	return &mycorrhizal{
		nodeId:        id,
		ctx:           ctx,
		wg:            &sync.WaitGroup{},
		cancel:        cancel,
		logger:        cfg.Logger,
		meter:         meter,
		httpClient:    httpClient,
		discoveryMode: cfg.DiscoveryMode,
		nodeAddrs:     cfg.NodeAddrs,
		singleMode:    cfg.SingleMode,
		nodosum:       ndsm,
		mycel:         mcl,
		debug:         cfg.Debug,
		readyChan:     make(chan any),
	}, nil
}

func (mc *mycorrhizal) Start() error {
	mc.logger.Info("mycorrhizal starting")
	wg := &sync.WaitGroup{}

	if mc.debug {
		go func() {
			log.Println(http.ListenAndServe("localhost:6060", nil))
		}()
	}

	wg.Go(func() {
		mc.nodosum.Start()
		err := mc.nodosum.Ready(time.Second * 30)
		if err != nil {
			mc.logger.Error(err.Error())
			mc.cancel()
		}
	})
	mc.wg.Add(1)

	wg.Go(func() {
		err := mc.mycel.Start()
		if err != nil {
			mc.logger.Error(err.Error())
			mc.cancel()
		}
		err = mc.mycel.Ready(time.Second * 30)
		if err != nil {
			mc.logger.Error(err.Error())
			mc.cancel()
		}
	})
	mc.wg.Add(1)
	wg.Wait()
	mc.logger.Info("mycorrhizal startup complete")
	mc.logger.Debug(fmt.Sprintf("mycorrhizal node id %s", mc.nodeId))
	close(mc.readyChan)
	return nil
}

func (mc *mycorrhizal) Ready(timeout time.Duration) error {
	if timeout == 0 {
		timeout = time.Second * 30
	}

	for {
		select {
		case <-mc.readyChan:
			return nil
		case <-time.After(timeout):
			return errors.New("timeout before mycorrhizal could reach ready state")
		case <-mc.ctx.Done():
			return errors.New("context canceled")
		}
	}
}

func (mc *mycorrhizal) Shutdown() error {
	mc.logger.Info("mycorrhizal shutting down")

	mc.wg.Go(func() {
		mc.nodosum.Shutdown()
		mc.wg.Done()
	})

	mc.wg.Go(func() {
		mc.mycel.Shutdown()
		mc.wg.Done()
	})

	if mc.debugHttpServer != nil {
		mc.debugHttpServer.Close()
	}

	mc.cancel()
	mc.logger.Debug("mycorrhizal shutting down waiting on goroutines...")
	mc.wg.Wait()
	mc.logger.Info("mycorrhizal shutdown complete")
	return nil
}

func (mc *mycorrhizal) RegisterApplication(uniqueIdentifier string) nodosum.Application {
	return mc.nodosum.RegisterApplication(uniqueIdentifier)
}

func (mc *mycorrhizal) GetApplication(uniqueIdentifier string) nodosum.Application {
	return mc.nodosum.GetApplication(uniqueIdentifier)
}

func (mc *mycorrhizal) Cache() mycel.Cache {
	return mc.mycel.Cache()
}
