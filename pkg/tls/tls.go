package tls

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
)

const clusterCACertPath = "/certs/ca.crt"

// GetClusterTLSConfig returns a *tls.Config that trusts the certs we're using
// to talk TLS to our internal components.
func GetClusterTLSConfig() (*tls.Config, error) {
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}

	if crt, err := os.ReadFile(clusterCACertPath); err != nil {
		return nil, fmt.Errorf("failed to read cluster CA cert: %w", err)
	} else if ok := pool.AppendCertsFromPEM(crt); !ok {
		return nil, errors.New("failed to append cert to pool")
	}

	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    pool,
	}, nil
}
