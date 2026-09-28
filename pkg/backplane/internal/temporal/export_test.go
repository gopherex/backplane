package temporal

import (
	"time"

	"github.com/gopherex/backplane/pkg/backplane/internal/backoff"
)

// UseFastRetry shortens the delay between connection and worker start
// attempts; call before Connect.
func (c *Client) UseFastRetry() {
	c.retry = backoff.Policy{Min: 50 * time.Millisecond, Max: 200 * time.Millisecond}
}
