package temporal

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/gopherex/xlog"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/pkg/backplane/internal/backoff"
	"github.com/gopherex/backplane/pkg/backplane/internal/env"
	"github.com/gopherex/backplane/pkg/backplane/internal/node"
)

// StartWorker runs the service's worker on its task queue: every declared
// activity, and CallHookWorkflow when hooks are declared. It does not wait
// for Temporal: the worker starts on g as soon as the connection is up,
// retrying a failed start.
func (c *Client) StartWorker(_ context.Context, g node.Group) error {
	hooks := false
	if m, err := c.p.Env.Manifest.Build(); err == nil {
		hooks = len(m.GetHooks()) > 0
	}

	g.Go(func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return nil
		case <-c.ready:
		}

		_ = backoff.Retry(ctx, c.retry, func(context.Context) error { return c.startWorker(hooks) },
			func(err error, in time.Duration) {
				c.log.Warn("temporal worker start failed, retrying", xlog.Err(err), xlog.Duration("retry_in", in))
			})

		return nil
	})

	return nil
}

// startWorker makes one attempt; a worker whose start failed is discarded.
func (c *Client) startWorker(hooks bool) error {
	c.mu.Lock()
	conn, done := c.client, c.closed || c.stopped
	c.mu.Unlock()

	if done || conn == nil {
		return nil
	}

	w := worker.New(conn, c.p.Service, worker.Options{
		WorkerStopTimeout: stopGrace,
		OnFatalError: func(err error) {
			c.log.Error("temporal worker stopped on a fatal error", xlog.Err(err))
		},
	})

	for name, h := range c.p.Env.Activities() {
		w.RegisterActivityWithOptions(c.activity(name, h), activity.RegisterOptions{Name: name})
	}

	if hooks {
		w.RegisterWorkflowWithOptions(callHook, workflow.RegisterOptions{Name: CallHookWorkflow})
	}

	for _, register := range c.p.Env.WorkerRegistrations() {
		register(w)
	}

	if err := w.Start(); err != nil {
		w.Stop()

		return fmt.Errorf("temporal: start worker: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed || c.stopped {
		go w.Stop()

		return nil
	}

	c.worker = w
	c.log.Info("temporal worker started", xlog.String("task_queue", c.p.Service))

	return nil
}

// StopWorker stops polling and lets running activities finish within ctx;
// past it the stop goes on in the background and is logged.
func (c *Client) StopWorker(ctx context.Context) error {
	c.mu.Lock()
	w := c.worker
	c.worker, c.stopped = nil, true
	c.mu.Unlock()

	if w == nil {
		return nil
	}

	done := make(chan struct{})

	go func() {
		defer close(done)

		w.Stop()
	}()

	select {
	case <-done:
	case <-ctx.Done():
		c.log.Warn("temporal worker still stopping at the end of the stop budget: activities in flight are abandoned")
	}

	return nil
}

// activity adapts a JSON handler to a Temporal activity over the envelope.
// The trace of the envelope parents the handler when Temporal headers did
// not carry the same trace. Errors: an application error passes as is;
// env.NonRetryable becomes a non-retryable one; anything else is retried by
// the caller's policy. Messages are prefixed with <service>.<Name>.
func (c *Client) activity(
	name string, h env.Handler,
) func(ctx context.Context, call *backplanev1.ActivityCall) (*backplanev1.ActivityResult, error) {
	full := c.p.Service + "." + name
	tracer := otel.GetTracerProvider().Tracer(scopeName)

	return func(ctx context.Context, call *backplanev1.ActivityCall) (*backplanev1.ActivityResult, error) {
		ctx, end := continueTrace(ctx, tracer, full, call.GetTrace())
		defer end()

		out, err := h(ctx, call.GetPayload())
		if err != nil {
			return nil, activityError(full, err)
		}

		return &backplanev1.ActivityResult{Payload: out}, nil
	}
}

func activityError(full string, err error) error {
	var (
		app    *temporal.ApplicationError
		marked env.NonRetryableError
	)

	switch {
	case errors.As(err, &app):
		return err
	case errors.As(err, &marked):
		return final(full+": "+err.Error(), NonRetryableType)
	default:
		return fmt.Errorf("%s: %w", full, err)
	}
}

// continueTrace starts a span under the envelope's trace when ctx is not
// already in it (the caller's Temporal headers did not carry it), linked to
// the span ctx has.
func continueTrace(
	ctx context.Context, tracer trace.Tracer, name string, carrier map[string]string,
) (context.Context, func()) {
	if len(carrier) == 0 {
		return ctx, func() {}
	}

	parent := w3c{}.Extract(context.Background(), propagation.MapCarrier(carrier))
	remote, current := trace.SpanContextFromContext(parent), trace.SpanContextFromContext(ctx)

	if !remote.IsValid() || remote.TraceID() == current.TraceID() {
		return ctx, func() {}
	}

	opts := []trace.SpanStartOption{trace.WithSpanKind(trace.SpanKindConsumer)}
	if current.IsValid() {
		opts = append(opts, trace.WithLinks(trace.Link{SpanContext: current}))
	}

	if b := baggage.FromContext(parent); b.Len() > 0 {
		ctx = baggage.ContextWithBaggage(ctx, b)
	}

	ctx, span := tracer.Start(trace.ContextWithRemoteSpanContext(ctx, remote), "activity "+name, opts...)

	return ctx, func() { span.End() }
}
