package executor

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	nexuspb "go.temporal.io/api/nexus/v1"
	"go.temporal.io/api/operatorservice/v1"
	"go.temporal.io/sdk/client"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/gopherex/xlog"
)

// endpointName is what Temporal accepts as an endpoint name; a service
// name ([a-z][a-z0-9-]*) fails it only by ending in "-".
var endpointName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9-]*[a-zA-Z0-9]$`)

// endpointPage is the page size of the endpoint listing.
const endpointPage = 100

// ensureEndpoints makes the endpoint <service> of every service in set
// target backplane's queue: created when missing (another replica creating
// it first is fine), updated when it targets something else. Endpoints of
// services no longer in the registry are left alone.
func (x *Executor) ensureEndpoints(ctx context.Context, c client.Client, set hookSet) error {
	have, err := endpoints(ctx, c)
	if err != nil {
		return err
	}

	var errs []error

	for _, svc := range set.services() {
		if !endpointName.MatchString(svc) {
			x.warnName(svc)

			continue
		}

		e, ok := have[svc]

		switch {
		case !ok:
			errs = append(errs, x.createEndpoint(ctx, c, svc))
		case !x.targets(e):
			errs = append(errs, x.updateEndpoint(ctx, c, e))
		}
	}

	return errors.Join(errs...)
}

// target is where every hook endpoint points: backplane's queue.
func (x *Executor) target() *nexuspb.EndpointTarget {
	return &nexuspb.EndpointTarget{Variant: &nexuspb.EndpointTarget_Worker_{
		Worker: &nexuspb.EndpointTarget_Worker{Namespace: x.ns, TaskQueue: x.queue},
	}}
}

func (x *Executor) targets(e *nexuspb.Endpoint) bool {
	w := e.GetSpec().GetTarget().GetWorker()

	return w.GetNamespace() == x.ns && w.GetTaskQueue() == x.queue
}

func (x *Executor) createEndpoint(ctx context.Context, c client.Client, svc string) error {
	_, err := c.OperatorService().CreateNexusEndpoint(ctx, &operatorservice.CreateNexusEndpointRequest{
		Spec: &nexuspb.EndpointSpec{Name: svc, Target: x.target()},
	})

	switch status.Code(err) {
	case codes.OK:
		x.Log().Info("nexus endpoint created", xlog.String("endpoint", svc), xlog.String("task_queue", x.queue))

		return nil
	case codes.AlreadyExists:
		return nil // another replica was first
	default:
		return fmt.Errorf("executor: create nexus endpoint %s: %w", svc, err)
	}
}

func (x *Executor) updateEndpoint(ctx context.Context, c client.Client, e *nexuspb.Endpoint) error {
	name := e.GetSpec().GetName()
	was := e.GetSpec().GetTarget().GetWorker()

	_, err := c.OperatorService().UpdateNexusEndpoint(ctx, &operatorservice.UpdateNexusEndpointRequest{
		Id: e.GetId(), Version: e.GetVersion(),
		Spec: &nexuspb.EndpointSpec{Name: name, Description: e.GetSpec().GetDescription(), Target: x.target()},
	})

	switch status.Code(err) {
	case codes.OK:
		x.Log().Warn("nexus endpoint retargeted to backplane", xlog.String("endpoint", name),
			xlog.String("was_namespace", was.GetNamespace()), xlog.String("was_task_queue", was.GetTaskQueue()),
			xlog.String("task_queue", x.queue))

		return nil
	case codes.FailedPrecondition, codes.AlreadyExists:
		return nil // changed meanwhile, by another replica: the next pass sees it
	default:
		return fmt.Errorf("executor: update nexus endpoint %s: %w", name, err)
	}
}

func (x *Executor) warnName(svc string) {
	x.mu.Lock()
	seen := x.warned[svc]
	x.warned[svc] = true
	x.mu.Unlock()

	if !seen {
		x.Log().Warn("service name cannot be a Nexus endpoint: its hooks are not reachable", xlog.String("service", svc))
	}
}

// endpoints are all Nexus endpoints by name.
func endpoints(ctx context.Context, c client.Client) (map[string]*nexuspb.Endpoint, error) {
	out := map[string]*nexuspb.Endpoint{}

	var token []byte

	for {
		res, err := c.OperatorService().ListNexusEndpoints(ctx, &operatorservice.ListNexusEndpointsRequest{
			PageSize: endpointPage, NextPageToken: token,
		})
		if err != nil {
			return nil, fmt.Errorf("executor: list nexus endpoints: %w", err)
		}

		for _, e := range res.GetEndpoints() {
			out[e.GetSpec().GetName()] = e
		}

		if token = res.GetNextPageToken(); len(token) == 0 {
			return out, nil
		}
	}
}
