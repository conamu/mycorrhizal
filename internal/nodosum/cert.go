package nodosum

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"time"
)

func (n *Nodosum) getCaCert() (*x509.Certificate, error) {
	caCertItemId := "mftxwy3by3yntleru5pwuqr2va"
	caCertData, err := n.getFileFromOnePassword(n.ctx, caCertItemId)
	if err != nil {
		return nil, err
	}

	block, _ := pem.Decode(caCertData)

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}

	return cert, nil
}

func (n *Nodosum) getCaKey() (*rsa.PrivateKey, error) {
	caKeyItemId := "elhn5uc4ydmu5ix4fpxng52y7a"
	caKeyData, err := n.getFileFromOnePassword(n.ctx, caKeyItemId)
	if err != nil {
		return nil, err
	}

	block, _ := pem.Decode(caKeyData)

	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}

	return key.(*rsa.PrivateKey), nil
}

func (n *Nodosum) getFileFromOnePassword(ctx context.Context, id string) ([]byte, error) {
	item, err := n.onePasswordClient.Items().Get(ctx, "r7muqexb7plbxorxeh5dgqmoi4", id)
	if err != nil {
		return nil, err
	}

	fileData, err := n.onePasswordClient.Items().Files().Read(ctx, item.VaultID, item.ID, *item.Document)
	if err != nil {
		return nil, err
	}

	return fileData, nil
}

func (n *Nodosum) generateNodeCert(additionalIPs ...net.IP) (*tls.Certificate, *x509.Certificate, error) {

	var caCert *x509.Certificate
	var caKey *rsa.PrivateKey
	var err error

	if n.tlsCaKey == nil || n.tlsCaCert == nil {
		caKey, err = n.getCaKey()
		if err != nil {
			return nil, nil, err
		}

		caCert, err = n.getCaCert()
		if err != nil {
			return nil, nil, err
		}
	} else {
		caCert = n.tlsCaCert
		caKey = n.tlsCaKey
	}

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
	certBytes, err := x509.CreateCertificate(rand.Reader, template, caCert, &nodeKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create certificate: %w", err)
	}

	// Create TLS certificate
	tlsCert := &tls.Certificate{
		Certificate: [][]byte{certBytes},
		PrivateKey:  nodeKey,
	}

	return tlsCert, caCert, nil
}
