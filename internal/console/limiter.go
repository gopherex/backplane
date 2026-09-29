package console

import (
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Brute force protection of the login (§11.3): a token bucket per client
// address plus a global backoff after consecutive failures from anywhere.
const (
	// attemptEvery refills one login attempt per address; attemptBurst
	// attempts may come at once.
	attemptEvery = 12 * time.Second
	attemptBurst = 5
	// After globalThreshold consecutive failures (any address) every login
	// waits globalBase, doubling per further failure up to globalMax; a
	// success resets it.
	globalThreshold = 10
	globalBase      = time.Second
	globalMax       = time.Minute
	// Addresses unseen for addrForget are dropped once maxAddrs are held.
	maxAddrs   = 10000
	addrForget = 10 * time.Minute
	// maxDoublings keeps the shift of the global backoff in range.
	maxDoublings = 16
)

// limiter is shared by pointer: it holds a lock.
type limiter struct {
	mu       sync.Mutex
	addrs    map[string]*addrLimit
	failures int
	lastFail time.Time
}

type addrLimit struct {
	lim  *rate.Limiter
	seen time.Time
}

func newLimiter() *limiter { return &limiter{addrs: map[string]*addrLimit{}} }

// allow takes one attempt for addr at now; when refused it reports how long
// to wait.
func (l *limiter) allow(addr string, now time.Time) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.failures >= globalThreshold {
		wait := globalBase << min(l.failures-globalThreshold, maxDoublings)
		if until := l.lastFail.Add(min(wait, globalMax)); now.Before(until) {
			return until.Sub(now), false
		}
	}

	a, ok := l.addrs[addr]
	if !ok {
		if len(l.addrs) >= maxAddrs {
			l.forget(now)
		}

		a = &addrLimit{lim: rate.NewLimiter(rate.Every(attemptEvery), attemptBurst)}
		l.addrs[addr] = a
	}

	a.seen = now
	if !a.lim.AllowN(now, 1) {
		missing := 1 - a.lim.TokensAt(now)

		return time.Duration(missing * float64(attemptEvery)), false
	}

	return 0, true
}

// forget drops addresses unseen for addrForget; l.mu is held.
func (l *limiter) forget(now time.Time) {
	for k, a := range l.addrs {
		if now.Sub(a.seen) > addrForget {
			delete(l.addrs, k)
		}
	}
}

// failed records a wrong token.
func (l *limiter) failed(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.failures++
	l.lastFail = now
}

// succeeded resets the global backoff.
func (l *limiter) succeeded() {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.failures = 0
}

// proxies are the trusted proxies whose X-Forwarded-For counts.
type proxies struct{ prefixes []netip.Prefix }

// parseProxies takes addresses and CIDRs (validated by the server's
// configuration); unparsable entries are skipped.
func parseProxies(list []string) proxies {
	var p proxies

	for _, s := range list {
		if pfx, err := netip.ParsePrefix(s); err == nil {
			p.prefixes = append(p.prefixes, pfx.Masked())

			continue
		}

		if a, err := netip.ParseAddr(s); err == nil {
			p.prefixes = append(p.prefixes, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
		}
	}

	return p
}

func (p proxies) trusted(a netip.Addr) bool {
	a = a.Unmap()
	for _, pfx := range p.prefixes {
		if pfx.Contains(a) {
			return true
		}
	}

	return false
}

// client is the address of the client behind r: the peer, or — while the
// peer is a trusted proxy — the X-Forwarded-For entries from the right,
// skipping trusted proxies, up to the first untrusted one.
func (p proxies) client(r *http.Request) string {
	host := r.RemoteAddr
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		host = ap.Addr().Unmap().String()
	}

	peer, err := netip.ParseAddr(host)
	if err != nil || !p.trusted(peer) {
		return host
	}

	var hops []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(v, ",")...)
	}

	current := peer

	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}

		current = a.Unmap()
		if !p.trusted(current) {
			break
		}
	}

	return current.String()
}
