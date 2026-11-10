// Package webdavx holds the WebDAV preflight check.
package webdavx

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"time"

	"tern/internal/model"
)

const checkTimeout = 15 * time.Second

// Check sends one PROPFIND with Depth: 0 and classifies the answer. It never
// follows redirects: the credentials are for the configured server only.
// The result is nil or a *model.KindError.
func Check(ctx context.Context, rawURL, user, pass string) error {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", rawURL, nil)
	if err != nil {
		return &model.KindError{Kind: model.KindConfig, Msg: "the WebDAV address is not a valid URL"}
	}
	req.Header.Set("Depth", "0")
	if user != "" || pass != "" {
		req.SetBasicAuth(user, pass)
	}
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		TLSHandshakeTimeout:   checkTimeout,
		ResponseHeaderTimeout: checkTimeout,
		DisableKeepAlives:     true,
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{
		Transport:     tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		if isCertError(err) {
			return &model.KindError{Kind: model.KindConfig, Msg: "the server's TLS certificate is not trusted", Err: err}
		}
		return &model.KindError{Kind: model.KindNetwork, Msg: "cannot reach the WebDAV server", Err: err}
	}
	resp.Body.Close()

	switch st := resp.StatusCode; {
	case st == http.StatusMultiStatus:
		return nil
	case st == http.StatusUnauthorized || st == http.StatusForbidden:
		return &model.KindError{Kind: model.KindAuth, Msg: fmt.Sprintf("the WebDAV server refused the credentials (HTTP %d)", st)}
	case st == http.StatusBadGateway || st == http.StatusServiceUnavailable || st == http.StatusGatewayTimeout:
		return &model.KindError{Kind: model.KindNetwork, Msg: fmt.Sprintf("the WebDAV server is unavailable (HTTP %d)", st)}
	case st >= 300 && st < 400:
		return &model.KindError{Kind: model.KindConfig,
			Msg: fmt.Sprintf("the address redirects elsewhere (HTTP %d); enter the final WebDAV address", st)}
	default:
		return &model.KindError{Kind: model.KindConfig,
			Msg: fmt.Sprintf("the address does not answer as WebDAV (HTTP %d to PROPFIND)", st)}
	}
}

func isCertError(err error) bool {
	var (
		verify   *tls.CertificateVerificationError
		unknown  x509.UnknownAuthorityError
		hostname x509.HostnameError
		invalid  x509.CertificateInvalidError
	)
	return errors.As(err, &verify) || errors.As(err, &unknown) || errors.As(err, &hostname) || errors.As(err, &invalid)
}
