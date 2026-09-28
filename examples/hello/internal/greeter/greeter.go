// Package greeter is the service's domain component: it serves HelloService
// over every public protocol with one implementation.
package greeter

import (
	"context"
	"fmt"
	"strings"
	"text/template"
	"time"

	"go.opentelemetry.io/otel/metric"
	"google.golang.org/grpc"

	"github.com/gopherex/xlog"

	"github.com/gopherex/backplane/examples/hello/internal/store"
	hellov1 "github.com/gopherex/backplane/examples/hello/proto/hello/v1"
	"github.com/gopherex/backplane/pkg/backplane/config"
	"github.com/gopherex/backplane/pkg/backplane/deps"
	"github.com/gopherex/backplane/pkg/backplane/event"
)

const (
	countdownTick = 100 * time.Millisecond
	reportEvery   = time.Minute
)

// Config of the greeter.
type Config struct {
	Salute string `json:"salute" schemapb:"default=Hello;description=Word before the name"`
	// Live: editable from the console, applied without restart.
	Suffix  config.Live[string] `json:"suffix"  schemapb:"default=!;description=Appended to every greeting"`
	Excited config.Live[bool]   `json:"excited" schemapb:"default=false"`
}

// Greeted is published after every greeting.
type Greeted struct {
	Name  string `json:"name"`
	Count uint64 `json:"count"`
}

// Greeter is a component: a named node with its own logger, tracer and meter.
type Greeter struct {
	hellov1.UnimplementedHelloServiceServer
	deps.Component

	cfg       *Config
	db        deps.Dependency[*store.DB]
	greeted   *event.Ref[Greeted]
	templates deps.Singleton[*template.Template]
	greetings metric.Int64Counter
}

// New creates the greeter under parent; everything it needs is passed in.
func New(parent deps.Scope, cfg *Config, db deps.Dependency[*store.DB], greeted *event.Ref[Greeted]) (*Greeter, error) {
	g := &Greeter{Component: deps.NewComponent(parent, "greeter"), cfg: cfg, db: db, greeted: greeted}

	greetings, err := g.Meter().Int64Counter("hello.greetings", metric.WithDescription("Greetings served"))
	if err != nil {
		return nil, fmt.Errorf("greeter metrics: %w", err)
	}

	g.greetings = greetings

	// Built on first use, closed at stop; not part of readiness.
	g.templates = deps.NewSingleton(g, deps.Func(parseTemplates), deps.Name("templates"))

	// Background work under the service lifecycle, started with the tree.
	g.Go(g.report)
	cfg.Suffix.Watch(func(s string) { g.Log().Info("suffix changed", xlog.String("suffix", s)) })

	return g, nil
}

// Register is the one registration for gRPC, Connect and ws-proto.
func (g *Greeter) Register(r grpc.ServiceRegistrar) { hellov1.RegisterHelloServiceServer(r, g) }

// Text greets name.
func (g *Greeter) Text(ctx context.Context, name string) (string, error) {
	var text string

	err := g.Span(ctx, "greet", func(ctx context.Context) error {
		tmpl, err := g.templates.Get(ctx)
		if err != nil {
			return fmt.Errorf("templates: %w", err)
		}

		count := g.db.Get().Inc(ctx, name)

		var b strings.Builder
		if err := tmpl.Execute(&b, g.view(name)); err != nil {
			return fmt.Errorf("render: %w", err)
		}

		text = b.String()

		g.greetings.Add(ctx, 1)

		if err := g.greeted.Publish(ctx, Greeted{Name: name, Count: count}, event.Key(name)); err != nil {
			g.Log().Ctx().Debug(ctx, "greeted not published", xlog.Err(err))
		}

		return nil
	})
	if err != nil {
		return "", fmt.Errorf("greeter: %w", err)
	}

	return text, nil
}

// Greet implements HelloService.
func (g *Greeter) Greet(ctx context.Context, req *hellov1.GreetRequest) (*hellov1.GreetResponse, error) {
	text, err := g.Text(ctx, req.GetName())
	if err != nil {
		return nil, err
	}

	return &hellov1.GreetResponse{Greeting: text}, nil
}

// Countdown implements HelloService.
func (g *Greeter) Countdown(
	req *hellov1.CountdownRequest, stream grpc.ServerStreamingServer[hellov1.CountdownResponse],
) error {
	for left := req.GetFrom(); ; left-- {
		if err := stream.Send(&hellov1.CountdownResponse{Left: left}); err != nil || left == 0 {
			return err //nolint:wrapcheck // gRPC status passes through
		}

		select {
		case <-stream.Context().Done():
			return stream.Context().Err() //nolint:wrapcheck // cancellation passes through
		case <-time.After(countdownTick):
		}
	}
}

type view struct {
	Salute, Name, Suffix string
	Excited              bool
}

func (g *Greeter) view(name string) view {
	return view{Salute: g.cfg.Salute, Name: name, Suffix: g.cfg.Suffix.Get(), Excited: g.cfg.Excited.Get()}
}

func (g *Greeter) report(ctx context.Context) error {
	t := time.NewTicker(reportEvery)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			g.Log().Ctx().Info(ctx, "greetings so far", xlog.Uint64("total", g.db.Get().Total()))
		}
	}
}

func parseTemplates(context.Context) (*template.Template, error) {
	tmpl, err := template.New("greeting").Parse(`{{.Salute}}, {{.Name}}{{.Suffix}}{{if .Excited}}!!{{end}}`)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}

	return tmpl, nil
}
