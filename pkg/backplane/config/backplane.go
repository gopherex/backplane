package config

import "time"

// Backplane is the SDK's own block, part of the service configuration:
//
//	type Config struct {
//	    config.Backplane `json:"backplane"`
//	    DB string        `json:"db"`
//	}
//
// Its environment names are fixed (BACKPLANE_*) for every service. Every
// dependency is optional for a service: an empty address means "without it".
type Backplane struct {
	Consul   Consul   `json:"consul"`
	NATS     NATS     `json:"nats"`
	Temporal Temporal `json:"temporal"`
	Shutdown Shutdown `json:"shutdown"`

	// Instance id; default <service>-<hostname>.
	Instance string `json:"instance,omitempty"`
	// Address other components reach this instance at; default POD_IP, then
	// the hostname's address.
	Advertise string `json:"advertise,omitempty"`
	// deployment.environment.name for telemetry; exporters use OTEL_*.
	Environment string `json:"environment,omitempty"`
	// Platform port: internal API, health, probes, UI bundle.
	InternalPort int64 `json:"internal_port" schemapb:"default=9400;gte=1;lte=65535"`
	// Shared secret the console relay presents; empty disables the check.
	InternalSecret Secret `json:"internal_secret,omitempty"`
	// Default listener of managed public routes.
	PublicPort int64  `json:"public_port" schemapb:"default=8080;gte=1;lte=65535"`
	LogLevel   string `json:"log_level"   schemapb:"default=info"`
}

// Backplaner is satisfied by any struct embedding Backplane.
type Backplaner interface{ BackplaneConfig() Backplane }

// BackplaneConfig returns the block; promoted to the embedding struct.
func (b Backplane) BackplaneConfig() Backplane { return b }

// Consul connection.
type Consul struct {
	Addr  string `json:"addr,omitempty"`
	Token Secret `json:"token,omitempty"`
	// false when the deployment registers the service itself.
	Register bool `json:"register" schemapb:"default=true"`
}

// Enabled reports whether Consul is configured.
func (c Consul) Enabled() bool { return c.Addr != "" }

// NATS connection and the streams the service owns: its events
// bp_<service> (applied when a service that declares events starts) and
// its dead letters bp_dlq_<service> (applied when its reactors start).
// Zero max_age, max_bytes or dlq_max_age mean unlimited.
type NATS struct {
	URL   string `json:"url,omitempty"`
	Creds Secret `json:"creds,omitempty"`

	// Retention of the service's events.
	MaxAge time.Duration `json:"max_age" schemapb:"default=168h"`
	// Size of the event stream in bytes; the oldest events go first.
	MaxBytes int64 `json:"max_bytes" schemapb:"default=0;gte=0"`
	// Replicas of both streams (JetStream cluster).
	Replicas int64 `json:"replicas" schemapb:"default=1;gte=1;lte=5"`
	// Window in which a repeated publish (same ce-id) is dropped; at most
	// max_age.
	DedupWindow time.Duration `json:"dedup_window" schemapb:"default=2m"`
	// Retention of the dead letters of the service's reactors.
	DLQMaxAge time.Duration `json:"dlq_max_age" schemapb:"default=720h"`
}

// Enabled reports whether NATS is configured.
func (n NATS) Enabled() bool { return n.URL != "" }

// Temporal connection. Field is `ns`: `namespace` is a CEL reserved word.
type Temporal struct {
	Addr      string `json:"addr,omitempty"`
	Namespace string `json:"ns"             schemapb:"default=default"`
	Worker    Worker `json:"worker"`
}

// Worker on the service's task queue: an operational setting, the same
// code runs with any of it. A zero limit is Temporal's default.
type Worker struct {
	// false: this replica runs no worker (activities, hooks raised outside
	// workflows and the author's workflows are served by replicas that do);
	// it still reconciles schedules and uses the client.
	Enabled bool `json:"enabled" schemapb:"default=true"`
	// Activities executing at once.
	MaxConcurrentActivities int64 `json:"max_concurrent_activities" schemapb:"default=0;gte=0"`
	// Workflow tasks executing at once.
	MaxConcurrentWorkflowTasks int64 `json:"max_concurrent_workflow_tasks" schemapb:"default=0;gte=0"`
	// Pollers of the activity and workflow task queues.
	ActivityPollers int64 `json:"activity_pollers" schemapb:"default=0;gte=0"`
	WorkflowPollers int64 `json:"workflow_pollers" schemapb:"default=0;gte=0"`
}

// Enabled reports whether Temporal is configured.
func (t Temporal) Enabled() bool { return t.Addr != "" }

// Shutdown of the instance: readiness drops, Consul deregisters, then after
// Drain the listeners stop accepting and in-flight work finishes; everything
// within Timeout. Keep terminationGracePeriodSeconds above Timeout.
type Shutdown struct {
	Timeout time.Duration `json:"timeout" schemapb:"default=25s"`
	// Time for load balancers to notice the instance left before its
	// listeners close.
	Drain time.Duration `json:"drain" schemapb:"default=3s"`
}
