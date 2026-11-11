package main

import (
	"crypto/tls"
	"net"
	"net/http"
	"time"

	"tern/internal/config"
)

// healthcheck asks the node configured by getenv for /api/v1/status over the
// loopback interface and returns the process exit code: 0 when it answers
// 200, 1 otherwise. The image's HEALTHCHECK runs it as `tern --healthcheck`.
func healthcheck(getenv func(string) string) int {
	cfg, err := config.Load(getenv)
	if err != nil {
		return 1
	}
	host, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return 1
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1" // listening on every address: ask this machine
	}
	scheme := "http"
	transport := &http.Transport{Proxy: nil}
	if cfg.TLSCert != "" {
		// The certificate names the address users reach, not loopback.
		scheme = "https"
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // loopback only
	}
	client := &http.Client{Transport: transport, Timeout: 4 * time.Second}
	resp, err := client.Get(scheme + "://" + net.JoinHostPort(host, port) + "/api/v1/status")
	if err != nil {
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
