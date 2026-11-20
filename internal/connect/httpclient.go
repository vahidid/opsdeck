package connect

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"strings"
	"time"
)

// APIClient builds an HTTP client that reaches an API the way dial says:
// through the given dialer, or through the environment's proxy when dial is
// nil. A CA certificate in PEM form ("" = none) is added to the system pool,
// so public roots keep working; certSubject names that certificate in the
// error message ("the connector's CA certificate"). respHeaderTimeout of 0
// leaves the transport default.
func APIClient(dial DialFunc, caPEM, certSubject string, clientTimeout, respHeaderTimeout time.Duration) (*http.Client, error) {
	tr := &http.Transport{TLSHandshakeTimeout: 20 * time.Second, ForceAttemptHTTP2: true, IdleConnTimeout: 30 * time.Second}
	if dial != nil {
		tr.DialContext = dial
	} else {
		tr.Proxy = http.ProxyFromEnvironment
	}
	if respHeaderTimeout > 0 {
		tr.ResponseHeaderTimeout = respHeaderTimeout
	}
	if ca := strings.TrimSpace(caPEM); ca != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM([]byte(ca)) {
			return nil, errors.New(certSubject + " is not valid PEM")
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool}
	}
	return &http.Client{Transport: tr, Timeout: clientTimeout}, nil
}
