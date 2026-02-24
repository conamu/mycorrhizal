package nodosum

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"time"
)

func parseCAPEM(certPEM, keyPEM []byte) (*x509.Certificate, *rsa.PrivateKey, error) {
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, nil, errors.New("CaCertPEM: failed to decode PEM block")
	}
	caCert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("CaCertPEM: %w", err)
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, nil, errors.New("CaKeyPEM: failed to decode PEM block")
	}
	var caKey *rsa.PrivateKey
	switch keyBlock.Type {
	case "RSA PRIVATE KEY":
		caKey, err = x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
		if err != nil {
			return nil, nil, fmt.Errorf("CaKeyPEM (PKCS1): %w", err)
		}
	case "PRIVATE KEY":
		parsed, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
		if err != nil {
			return nil, nil, fmt.Errorf("CaKeyPEM (PKCS8): %w", err)
		}
		var ok bool
		caKey, ok = parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, nil, errors.New("CaKeyPEM: only RSA private keys are supported")
		}
	default:
		return nil, nil, fmt.Errorf("CaKeyPEM: unsupported PEM block type %q", keyBlock.Type)
	}

	if !caCert.IsCA {
		return nil, nil, errors.New("CaCertPEM: certificate is not a CA certificate")
	}

	certPubKey, ok := caCert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return nil, nil, errors.New("CaCertPEM: certificate public key is not RSA")
	}
	if certPubKey.N.Cmp(caKey.PublicKey.N) != 0 || certPubKey.E != caKey.PublicKey.E {
		return nil, nil, errors.New("CaKeyPEM: private key does not match certificate public key")
	}

	return caCert, caKey, nil
}

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
			Organization: []string{"Mycorrhizal Cluster"},
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
