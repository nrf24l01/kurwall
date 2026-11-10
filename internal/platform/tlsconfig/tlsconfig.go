// Package tlsconfig builds the mutual-TLS configurations used by Kurwall.
package tlsconfig

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
)

// Server builds the coordinator listener TLS configuration. When
// requireClientCert is true every agent must present a certificate issued by
// one of caFiles, and its CN is the node identity.
func Server(caFiles []string, certFile, keyFile string, requireClientCert bool) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load TLS key pair: %w", err)
	}
	pool, err := loadCAFiles(caFiles)
	if err != nil {
		return nil, err
	}
	clientAuth := tls.VerifyClientCertIfGiven
	if requireClientCert {
		clientAuth = tls.RequireAndVerifyClientCert
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   clientAuth,
	}, nil
}

// Client builds the agent-side TLS configuration.
func Client(caFile, certFile, keyFile, serverName string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load agent key pair: %w", err)
	}
	pool, err := loadCAFiles([]string{caFile})
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		ServerName:   serverName,
	}, nil
}

// ReadCertificate loads the first certificate from a PEM file.
func ReadCertificate(path string) (*x509.Certificate, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read certificate: %w", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("%s contains no certificate", path)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse certificate %s: %w", path, err)
	}
	return cert, nil
}

func loadCAFiles(paths []string) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read CA %s: %w", path, err)
		}
		if !pool.AppendCertsFromPEM(raw) {
			return nil, fmt.Errorf("CA file %s contains no valid certificates", path)
		}
	}
	return pool, nil
}
