package lifecycle_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/gopherex/backplane/pkg/backplane/internal/lifecycle"
	"github.com/gopherex/backplane/pkg/backplane/internal/testlog"
)

type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) add(e string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.events = append(r.events, e)
}

func (r *recorder) get() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.events)
}

type comp struct {
	name     string
	rec      *recorder
	startErr error
	stopWait time.Duration
}

func (c comp) Name() string { return c.name }

func (c comp) Start(context.Context, lifecycle.Group) error {
	c.rec.add("start " + c.name)
	return c.startErr
}

func (c comp) Stop(ctx context.Context) error {
	select {
	case <-time.After(c.stopWait):
	case <-ctx.Done():
	}

	c.rec.add("stop " + c.name)

	return nil
}

func TestOrderAndCancel(t *testing.T) {
	t.Parallel()

	rec := &recorder{}
	ctx, cancel := context.WithCancel(context.Background())
	l := lifecycle.New(ctx, testlog.Discard(), time.Second)
	l.Add(comp{name: "a", rec: rec})
	l.Add(comp{name: "b", rec: rec})
	l.Go("worker", func(ctx context.Context) error {
		rec.add("go")
		<-ctx.Done()

		return ctx.Err()
	})

	done := make(chan error)

	go func() { done <- l.Run(ctx) }()

	time.Sleep(50 * time.Millisecond)
	cancel()

	if err := <-done; err != nil {
		t.Fatal(err)
	}

	want := []string{"start a", "start b", "go", "stop b", "stop a"}
	if got := rec.get(); !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestGoroutineFailureStops(t *testing.T) {
	t.Parallel()

	rec := &recorder{}
	l := lifecycle.New(context.Background(), testlog.Discard(), time.Second)
	l.Add(comp{name: "a", rec: rec})

	boom := errors.New("boom")

	l.Go("failing", func(context.Context) error { return boom })

	if err := l.Run(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("want boom, got %v", err)
	}

	if got := rec.get(); !slices.Equal(got, []string{"start a", "stop a"}) {
		t.Fatalf("got %v", got)
	}
}

func TestStartErrorStopsStarted(t *testing.T) {
	t.Parallel()

	rec := &recorder{}
	l := lifecycle.New(context.Background(), testlog.Discard(), time.Second)
	bad := errors.New("bad")

	l.Add(comp{name: "a", rec: rec})
	l.Add(comp{name: "b", rec: rec, startErr: bad})
	l.Add(comp{name: "c", rec: rec})

	if err := l.Run(context.Background()); !errors.Is(err, bad) {
		t.Fatalf("want bad, got %v", err)
	}

	if got := rec.get(); !slices.Equal(got, []string{"start a", "start b", "stop a"}) {
		t.Fatalf("got %v", got)
	}
}

func TestSingleBudget(t *testing.T) {
	t.Parallel()

	rec := &recorder{}
	ctx, cancel := context.WithCancel(context.Background())
	l := lifecycle.New(ctx, testlog.Discard(), 200*time.Millisecond)
	l.Add(comp{name: "slow", rec: rec, stopWait: time.Hour})
	l.Go("stuck", func(context.Context) error { select {} })
	cancel()

	begin := time.Now()

	err := l.Run(ctx)
	if !errors.Is(err, lifecycle.ErrStopTimeout) {
		t.Fatalf("want timeout, got %v", err)
	}

	if took := time.Since(begin); took > 400*time.Millisecond {
		t.Fatalf("stop took %v, budget 200ms", took)
	}
}

// A component blocked in Start (a dependency retrying) must not block
// shutdown.
func TestCancelDuringStart(t *testing.T) {
	t.Parallel()

	rec := &recorder{}
	ctx, cancel := context.WithCancel(context.Background())
	l := lifecycle.New(ctx, testlog.Discard(), time.Second)
	l.Add(comp{name: "a", rec: rec})
	l.Add(blocking{})

	done := make(chan error, 1)

	go func() { done <- l.Run(ctx) }()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("clean stop expected, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run blocked in Start")
	}

	if got := rec.get(); !slices.Equal(got, []string{"start a", "stop a"}) {
		t.Fatalf("got %v", got)
	}
}

type blocking struct{}

func (blocking) Name() string { return "blocking" }

func (blocking) Start(ctx context.Context, _ lifecycle.Group) error {
	<-ctx.Done()

	return ctx.Err()
}

func (blocking) Stop(context.Context) error { return nil }
