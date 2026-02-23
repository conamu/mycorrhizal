package nodosum

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"time"
)

func (n *Nodosum) generateNodeCert(additionalIPs ...net.IP) (*tls.Certificate, *x509.Certificate, error) {

	// Generate private key for this node
	nodeKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate node private key: %w", err)
	}

	// Generate random serial number
	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate serial number: %w", err)
	}

	// Build IP address list
	ipAddresses := []net.IP{net.ParseIP("127.0.0.1")}
	ipAddresses = append(ipAddresses, additionalIPs...)

	// Create certificate template
	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   n.nodeId,
			Organization: []string{"Mycorrizal Cluster"},
		},
		NotBefore: time.Now(),
		NotAfter:  time.Now().Add(365 * 24 * time.Hour), // 1 year validity
		KeyUsage:  x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth, // For accepting connections
			x509.ExtKeyUsageClientAuth, // For initiating connections (mutual TLS)
		},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost", n.nodeId},
		IPAddresses:           ipAddresses,
	}

	// Sign certificate with CA
	certBytes, err := x509.CreateCertificate(rand.Reader, template, n.tlsCaCert, &nodeKey.PublicKey, n.tlsCaKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create certificate: %w", err)
	}

	// Create TLS certificate
	tlsCert := &tls.Certificate{
		Certificate: [][]byte{certBytes},
		PrivateKey:  nodeKey,
	}

	return tlsCert, n.tlsCaCert, nil
}
