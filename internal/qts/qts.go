// Package qts checks sign-ins to QNAP's QTS. On a QNAP NAS tern runs behind
// QTS's own web server and opens in a QTS desktop window; an administrator
// who is signed in to QTS is not asked to sign in to tern again.
package qts

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DefaultAuthURL is QTS's session check, served by its internal web server
// on the loopback interface.
const DefaultAuthURL = "http://127.0.0.1:58080/cgi-bin/authLogin.cgi"

// SessionCookie is the cookie in which QTS keeps its session id.
const SessionCookie = "NAS_SID"

// CacheTTL is how long an answer about a session id is reused. Signing out
// of QTS ends access to tern at most this much later.
const CacheTTL = 30 * time.Second

const (
	maxCached = 256
	maxSIDLen = 128
)

// Checker asks QTS whether a session id belongs to a signed-in
// administrator. The zero value is not usable; use New.
type Checker struct {
	url    string
	client *http.Client
	now    func() time.Time

	mu    sync.Mutex
	cache map[string]entry
}

type entry struct {
	user  string
	admin bool
	until time.Time
}

// New returns a Checker that asks authURL (DefaultAuthURL when empty).
// now is the time source for the cache; nil means time.Now.
func New(authURL string, now func() time.Time) *Checker {
	if authURL == "" {
		authURL = DefaultAuthURL
	}
	if now == nil {
		now = time.Now
	}
	return &Checker{
		url: authURL,
		// Never through a proxy: the check is for this machine.
		client: &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}},
		now:    now,
		cache:  map[string]entry{},
	}
}

// Admin reports whether sid is a live QTS session of an administrator,
// and the user's name. A malformed sid is simply not signed in. err is set
// only when QTS could not be asked; such answers are not cached.
func (c *Checker) Admin(ctx context.Context, sid string) (user string, ok bool, err error) {
	if !validSID(sid) {
		return "", false, nil
	}
	now := c.now()
	c.mu.Lock()
	e, hit := c.cache[sid]
	c.mu.Unlock()
	if hit && now.Before(e.until) {
		return e.user, e.admin, nil
	}

	e, err = c.ask(ctx, sid)
	if err != nil {
		return "", false, err
	}
	e.until = now.Add(CacheTTL)
	c.mu.Lock()
	if len(c.cache) >= maxCached {
		for k, v := range c.cache {
			if !now.Before(v.until) {
				delete(c.cache, k)
			}
		}
		if len(c.cache) >= maxCached {
			clear(c.cache)
		}
	}
	c.cache[sid] = e
	c.mu.Unlock()
	return e.user, e.admin, nil
}

// validSID accepts what QTS hands out: letters and digits.
func validSID(sid string) bool {
	if sid == "" || len(sid) > maxSIDLen {
		return false
	}
	for _, r := range sid {
		if !('a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9') {
			return false
		}
	}
	return true
}

type authXML struct {
	AuthPassed string `xml:"authPassed"`
	Username   string `xml:"username"`
	IsAdmin    string `xml:"isAdmin"`
}

func (c *Checker) ask(ctx context.Context, sid string) (entry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url+"?sid="+url.QueryEscape(sid), nil)
	if err != nil {
		return entry{}, fmt.Errorf("qts: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return entry{}, fmt.Errorf("qts: ask authLogin.cgi: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return entry{}, fmt.Errorf("qts: authLogin.cgi answered HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return entry{}, fmt.Errorf("qts: read authLogin.cgi: %w", err)
	}
	var x authXML
	if err := xml.Unmarshal(body, &x); err != nil {
		return entry{}, fmt.Errorf("qts: authLogin.cgi answer is not XML: %w", err)
	}
	if strings.TrimSpace(x.AuthPassed) == "" {
		return entry{}, errors.New("qts: authLogin.cgi answer has no authPassed")
	}
	if strings.TrimSpace(x.AuthPassed) != "1" {
		return entry{}, nil
	}
	return entry{user: strings.TrimSpace(x.Username), admin: strings.TrimSpace(x.IsAdmin) == "1"}, nil
}
