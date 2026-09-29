package console_test

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gopherex/backplane/internal/console"
)

func TestBootstrapGenerated(t *testing.T) {
	t.Parallel()

	sessions := newMemSessions()
	e := newEnv(t, sessions, func(s *console.Settings) { s.AdminToken = "" })

	out := e.out.String()

	i := strings.Index(out, "bpat_")
	if i < 0 {
		t.Fatalf("no token printed: %q", out)
	}

	token := strings.Fields(out[i:])[0]
	if res, c := e.login(token); res.StatusCode != http.StatusOK || c == nil {
		t.Fatalf("login with the printed token: %s", res.Status)
	}

	// Another replica (or a restart) finds the token: nothing is printed.
	again := newEnv(t, sessions, func(s *console.Settings) { s.AdminToken = "" })
	if again.out.Len() != 0 {
		t.Fatalf("printed again: %q", again.out)
	}

	// The environment does not override a stored token.
	env := newEnv(t, sessions)
	if res, _ := env.login(adminToken); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("env token over a stored one: %s", res.Status)
	}
}

func TestLoginFlow(t *testing.T) {
	t.Parallel()

	e := newEnv(t, nil)

	if res, c := e.login("wrong"); res.StatusCode != http.StatusUnauthorized || c != nil {
		t.Fatalf("wrong token: %s", res.Status)
	}

	if res := e.request(http.MethodPost, "/auth/login", "not an object"); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad body: %s", res.Status)
	}

	if res, _ := e.login(adminToken, "Origin", "https://evil.example"); res.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin login: %s", res.Status)
	}

	login, c := e.login(adminToken, "Origin", e.origin())
	if login.StatusCode != http.StatusOK || c == nil {
		t.Fatalf("login: %s", login.Status)
	}

	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
		t.Fatalf("cookie flags: %+v", c)
	}

	for _, h := range []string{"Content-Security-Policy", "X-Content-Type-Options", "Strict-Transport-Security"} {
		if login.Header.Get(h) == "" {
			t.Fatalf("no %s", h)
		}
	}

	if login.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("cache-control %q", login.Header.Get("Cache-Control"))
	}

	if res := e.request(http.MethodGet, "/auth/session", nil, "Cookie", cookieHeader(c)); res.StatusCode != http.StatusOK {
		t.Fatalf("session: %s", res.Status)
	}

	if res := e.request(http.MethodGet, "/auth/session", nil); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("session without cookie: %s", res.Status)
	}

	if res := e.request(http.MethodPost, "/auth/logout", nil, "Cookie", cookieHeader(c)); res.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: %s", res.Status)
	}

	if res := e.request(http.MethodGet, "/auth/session", nil, "Cookie", cookieHeader(c)); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("session after logout: %s", res.Status)
	}
}

func TestInsecureCookieAndPrefix(t *testing.T) {
	t.Parallel()

	e := newEnv(t, nil, func(s *console.Settings) { s.InsecureCookie, s.Prefix = true, "/bp" })

	res := e.request(http.MethodPost, "/bp/auth/login", map[string]string{"token": adminToken})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("login under the prefix: %s", res.Status)
	}

	c := res.Cookies()[0]
	if c.Secure || c.Path != "/bp/" || res.Header.Get("Strict-Transport-Security") != "" {
		t.Fatalf("insecure cookie: %+v, hsts %q", c, res.Header.Get("Strict-Transport-Security"))
	}

	if e.console.Route() != "/bp/" {
		t.Fatalf("route %q", e.console.Route())
	}
}

func TestSessionExpiry(t *testing.T) {
	t.Parallel()

	e := newEnv(t, nil)
	session := func(c *http.Cookie) int {
		return e.request(http.MethodGet, "/auth/session", nil, "Cookie", cookieHeader(c)).StatusCode
	}

	// Idle: activity keeps it, an hour without ends it.
	idle := e.mustLogin()

	e.clock.Advance(console.IdleTimeout - time.Minute)

	if code := session(idle); code != http.StatusOK {
		t.Fatalf("active within the idle timeout: %d", code)
	}

	e.clock.Advance(console.IdleTimeout - time.Minute)

	if code := session(idle); code != http.StatusOK {
		t.Fatalf("activity extends it: %d", code)
	}

	e.clock.Advance(console.IdleTimeout)

	if code := session(idle); code != http.StatusUnauthorized {
		t.Fatalf("idle for the timeout: %d", code)
	}

	// Absolute: activity does not extend it past its lifetime.
	abs := e.mustLogin()
	for elapsed := time.Duration(0); elapsed < console.SessionTTL-console.IdleTimeout; elapsed += console.IdleTimeout / 2 {
		e.clock.Advance(console.IdleTimeout / 2)

		if code := session(abs); code != http.StatusOK {
			t.Fatalf("active session at %v: %d", elapsed, code)
		}
	}

	e.clock.Advance(console.IdleTimeout)

	if code := session(abs); code != http.StatusUnauthorized {
		t.Fatalf("past the lifetime: %d", code)
	}
}

func TestBruteForce(t *testing.T) {
	t.Parallel()

	e := newEnv(t, nil, func(s *console.Settings) { s.TrustedProxies = []string{"127.0.0.1"} })
	from := func(addr, token string) *http.Response {
		res, _ := e.login(token, "X-Forwarded-For", addr)

		return res
	}

	// Per address: a burst of 5, then 429 with Retry-After.
	for i := range 5 {
		if res := from("198.51.100.1", "wrong"); res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %s", i, res.Status)
		}
	}

	limited := from("198.51.100.1", adminToken)
	if limited.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("over the burst: %s", limited.Status)
	}

	if s, err := strconv.Atoi(limited.Header.Get("Retry-After")); err != nil || s < 1 {
		t.Fatalf("retry-after %q", limited.Header.Get("Retry-After"))
	}

	// Another address (through the trusted proxy) is not limited by it.
	if res := from("198.51.100.2", adminToken); res.StatusCode != http.StatusOK {
		t.Fatalf("other address: %s", res.Status)
	}

	// The bucket refills.
	e.clock.Advance(15 * time.Second)

	if res := from("198.51.100.1", adminToken); res.StatusCode != http.StatusOK {
		t.Fatalf("after refill: %s", res.Status)
	}

	// Global: consecutive failures from many addresses back everyone off.
	for i := range 10 {
		if res := from("203.0.113."+strconv.Itoa(i), "wrong"); res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("global attempt %d: %s", i, res.Status)
		}
	}

	if res := from("192.0.2.200", adminToken); res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("global backoff: %s", res.Status)
	}

	e.clock.Advance(2 * time.Second)

	if res := from("192.0.2.200", adminToken); res.StatusCode != http.StatusOK {
		t.Fatalf("after the backoff: %s", res.Status)
	}
}

// X-Forwarded-For counts only through trusted proxies.
func TestClientAddress(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		trusted []string
		xff     string
		want    string
	}{
		"untrusted peer":       {nil, "198.51.100.7", "127.0.0.1"},
		"trusted peer":         {[]string{"127.0.0.0/8"}, "198.51.100.7", "198.51.100.7"},
		"chain of proxies":     {[]string{"127.0.0.1", "10.0.0.0/8"}, "198.51.100.7, 10.1.2.3", "198.51.100.7"},
		"spoofed left entries": {[]string{"127.0.0.1"}, "1.1.1.1, 198.51.100.7", "198.51.100.7"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			sessions := newMemSessions()
			e := newEnv(t, sessions, func(s *console.Settings) { s.TrustedProxies = tc.trusted })

			if res, _ := e.login(adminToken, "X-Forwarded-For", tc.xff); res.StatusCode != http.StatusOK {
				t.Fatalf("login: %s", res.Status)
			}

			list, _ := sessions.List(t.Context())
			if len(list) != 1 || list[0].Address != tc.want {
				t.Fatalf("address: %+v, want %s", list, tc.want)
			}
		})
	}
}
