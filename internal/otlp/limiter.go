package otlp

import (
	"container/list"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const maxForwardedBytes = 4096

type ipEntry struct {
	address netip.Addr
	last    time.Time
	rate    *rate.Limiter
}

type ipLimits struct {
	mu      sync.Mutex
	entries map[netip.Addr]*list.Element
	order   list.List
	cfg     Config
}

// admit never evicts an active entry to admit a new address: address churn
// cannot reset somebody else's bucket or grow an unbounded map.
func (l *ipLimits) admit(address netip.Addr, now time.Time) int {
	l.mu.Lock()
	defer l.mu.Unlock()

	for element := l.order.Front(); element != nil; element = l.order.Front() {
		entry := element.Value.(*ipEntry) //nolint:forcetypeassert,errcheck // only ipEntry is inserted
		if now.Sub(entry.last) < l.cfg.IdleTTL {
			break
		}

		delete(l.entries, entry.address)
		l.order.Remove(element)
	}

	element := l.entries[address]
	if element == nil {
		if int64(len(l.entries)) >= l.cfg.MaxIPs {
			return http.StatusServiceUnavailable
		}

		perSecond := rate.Limit(float64(l.cfg.RatePerMinute) / time.Minute.Seconds())
		entry := &ipEntry{address: address, rate: rate.NewLimiter(perSecond, int(l.cfg.Burst))}
		element = l.order.PushBack(entry)
		l.entries[address] = element
	}

	entry := element.Value.(*ipEntry) //nolint:forcetypeassert,errcheck // only ipEntry is inserted
	entry.last = now

	l.order.MoveToBack(element)

	if !entry.rate.AllowN(now, 1) {
		return http.StatusTooManyRequests
	}

	return 0
}

func (h *Handler) clientIP(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}

	address, _ := netip.ParseAddr(host)
	address = address.Unmap()

	forwarded := r.Header.Get("X-Forwarded-For")
	if len(forwarded) > maxForwardedBytes {
		return address
	}

	hops := strings.Split(forwarded, ",")
	for i := len(hops) - 1; i >= 0 && h.trusted(address); i-- {
		next, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}

		address = next.Unmap()
	}

	return address
}

func (h *Handler) trusted(address netip.Addr) bool {
	for _, prefix := range h.proxies {
		if prefix.Contains(address) {
			return true
		}
	}

	return false
}
