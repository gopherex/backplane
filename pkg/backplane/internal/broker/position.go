package broker

import "sync"

// position follows how far a reactor got in its source stream, so a
// consumer deleted on the server is recreated where the old one stopped
// instead of at the stream's end: at the oldest message not settled yet,
// else right after the newest one seen.
type position struct {
	mu   sync.Mutex
	base uint64              // ack floor of the consumer when consumption started
	high uint64              // newest stream sequence delivered
	open map[uint64]struct{} // delivered, not yet acked or terminated
}

func newPosition() *position { return &position{open: map[uint64]struct{}{}} }

// started records the consumer's ack floor at the start of a consumption.
func (p *position) started(floor uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.base = max(p.base, floor)
}

// delivered records a delivery of stream sequence seq.
func (p *position) delivered(seq uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.open[seq] = struct{}{}
	p.high = max(p.high, seq)
}

// settled records that seq was acked or terminated.
func (p *position) settled(seq uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	delete(p.open, seq)
}

// resume is the stream sequence a recreated consumer starts at.
func (p *position) resume() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()

	var oldest uint64

	for seq := range p.open {
		if oldest == 0 || seq < oldest {
			oldest = seq
		}
	}

	if oldest != 0 {
		return oldest
	}

	return max(p.base, p.high) + 1
}
