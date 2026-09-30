package console

import (
	"net/http"
	"net/netip"
	"strings"
)

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
