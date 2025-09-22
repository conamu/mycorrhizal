package mycorrizal

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"log/slog"
	"net"
	"net/url"
	"os"
)

const (
	// DC_MODE_DNS_SD uses dns service discovery
	DC_MODE_DNS_SD = iota
	// DC_MODE_CONSUL uses Consul service API
	DC_MODE_CONSUL
	// DC_MODE_STATIC uses a static list of Addresses
	DC_MODE_STATIC
)

type Config struct {
	Ctx    context.Context
	Logger *slog.Logger
	/*
		DiscoveryMode can be one of

		Use dns service discovery from DiscoveryHost
		DC_MODE_DNS_SD

		Use Consul service API from DiscoveryHost
		DC_MODE_CONSUL

		Use a static list of Addresses from NodeAddrs
		DC_MODE_STATIC
	*/
	DiscoveryMode int
	/*
		DiscoveryHost is either the DNS service or the Consul API endpoint used.
		Specify "url:port" or "ip:port"
	*/
	DiscoveryHost *url.URL
	/*
		NodeAddrs specifies a static list of servers to connect to
		Mycorrizal will generally exclude connecting to its own network Address,
		so it's safe to include a complete list of all node addresses
	*/
	NodeAddrs []net.TCPAddr
	/*
		HttpClientTLSEnabled if true, supply HttpClientTLSCACert and HttpClientTLSCert
		to authenticate with Consul API
		Only needed if DiscoveryMode is set to DC_MODE_CONSUL
	*/
	HttpClientTLSEnabled bool
	HttpClientTLSCACert  *x509.CertPool
	HttpClientTLSCert    *tls.Certificate
	/*
		SingleMode disables all Cluster features but leaves the listener enabled for the CLI.
		A node in SingleMode will reject all connections besides ones identified as an Authenticated CLI instance.
	*/
	SingleMode bool
	ListenPort int
}

func GetDefaultConfig() *Config {

	return &Config{
		Ctx: context.Background(),
		Logger: slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
			Level: slog.LevelInfo,
		})),
		DiscoveryMode: DC_MODE_STATIC,
		SingleMode:    false,
		ListenPort:    6969,
	}
}
