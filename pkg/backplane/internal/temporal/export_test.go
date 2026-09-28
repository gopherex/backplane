package temporal

import (
	"time"

	"go.temporal.io/sdk/worker"

	"github.com/gopherex/backplane/pkg/backplane/internal/backoff"
)

// UseFastRetry shortens the delay between connection and worker start
// attempts; call before Connect.
func (c *Client) UseFastRetry() {
	c.retry = backoff.Policy{Min: 50 * time.Millisecond, Max: 200 * time.Millisecond}
}

// WorkerOptions are the options the worker is built with.
func (c *Client) WorkerOptions() worker.Options { return c.workerOptions() }
