# Deploying services and backplane

backplane never deploys anything. This is what a deployment provides so that
services on the SDK and backplane itself work; `docker-compose.yaml` at the
repository root is a complete example for development (platform-in-a-box).

## A service on the SDK

| what | how |
|---|---|
| identity | build with `-ldflags -X github.com/gopherex/backplane/pkg/backplane/build.Service=<name> -X ….Version=<v>` (see `make hello`) |
| static configuration | env `<SERVICE>_*`, or file `BACKPLANE_CONFIG_FILE` (YAML/JSON) |
| dynamic configuration | same file under `dynamic:`, env `<SERVICE>_DYNAMIC_*`; Consul KV `config/<service>/dynamic/` on top |
| SDK block | env `BACKPLANE_*`: `CONSUL_ADDR`, `CONSUL_TOKEN`, `CONSUL_REGISTER`, `INSTANCE`, `ADVERTISE`, `ENVIRONMENT`, `INTERNAL_PORT` (9400), `INTERNAL_SECRET`, `PUBLIC_PORT` (8080), `LOG_LEVEL`, `SHUTDOWN_TIMEOUT` (25s) |
| address | `BACKPLANE_ADVERTISE`, else `POD_IP` (Downward API in k8s), else the hostname's IPv4; must be reachable from the Consul agent (health check) and from Envoy |
| telemetry | standard `OTEL_*`; without `OTEL_EXPORTER_OTLP_ENDPOINT` export is off |
| ports | platform port (`INTERNAL_PORT`): never public — only Consul checks, probes and backplane reach it; public port(s): reached by Envoy |
| probes | `GET :9400/healthz/liveness`, `/healthz/readiness`, `/healthz/startup`; `grpc.health.v1` on the same port |
| shutdown | SIGTERM; the SDK stops within `SHUTDOWN_TIMEOUT` — keep the orchestrator's grace period above it |

Nothing but the identity is required: without Consul, NATS, Temporal or a
collector the service starts and serves, logging one warning per missing
dependency.

## Telemetry the console reads

backplane shows what it can find in the deployment's telemetry stack
(VictoriaMetrics / VictoriaLogs / VictoriaTraces in v0). Scrape into it:

| source | endpoint | labels expected |
|---|---|---|
| services | OTLP from the SDK (`OTEL_EXPORTER_OTLP_ENDPOINT`) | resource `service.name`, `service.instance.id`, `service.version`, `deployment.environment.name` |
| Envoy | `:9901/stats/prometheus` | `envoy_cluster_name` = service name |
| Temporal server | its Prometheus endpoint | `task_queue` = service name |
| NATS | `prometheus-nats-exporter` against `:8222` | `stream_name` = `bp_<service>` |

Without these the corresponding panels are empty; everything else works.

## Files here

- `envoy/envoy.yaml` — Envoy bootstrap: admin on 9901, everything else over
  xDS (ADS, delta) from backplane on port 18000.
