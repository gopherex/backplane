# Deploying services and backplane

backplane never deploys anything. This is what a deployment provides so that
services on the SDK work, and what the backplane server (`cmd/backplane`)
needs. `docker-compose.yaml` at the repository root is a complete example for
development: platform-in-a-box.

## A service on the SDK

| what | how |
|---|---|
| identity | build with `-ldflags "-X github.com/gopherex/backplane/pkg/backplane/build.Service=<name> -X ….Version=<v> -X ….Commit=<sha> -X ….Date=<rfc3339>"` (see `make hello`); without `Service` the name is the last element of the main package path |
| configuration | one struct: schema defaults < file `BACKPLANE_CONFIG_FILE` (YAML by `.yaml`/`.yml`, else JSON) < env `BACKPLANE_*` for the SDK block < env `<SERVICE>_*` for everything (`HELLO_GREETER_SUFFIX`; `<SERVICE>` is the name upper-cased, `-` and `.` as `_`) < Consul KV `config/<service>/<path>` |
| live configuration | only fields of type `config.Live[T]` change at runtime, from Consul KV `config/<service>/<path>` (nested as `/`, scalars as strings, containers as JSON); ordinary fields come from env and file only and change by a rollout |
| SDK block | env `BACKPLANE_*` — the full list with defaults is below and in `platform-design.md` §4.3 |
| address | `BACKPLANE_ADVERTISE`, else `POD_IP` (Downward API in k8s), else the hostname's IPv4; must be reachable from the Consul agent (health check) and from Envoy |
| telemetry | standard `OTEL_*`; a signal without an endpoint (`OTEL_EXPORTER_OTLP_ENDPOINT` or `OTEL_EXPORTER_OTLP_<SIGNAL>_ENDPOINT`) is not exported |
| ports | platform port (`BACKPLANE_INTERNAL_PORT`, 9400): never public — only Consul checks, probes and backplane reach it; public port(s) (`BACKPLANE_PUBLIC_PORT`, 8080, plus `route.Listen` addresses): reached by Envoy |
| probes | `GET :9400/healthz/liveness`, `/healthz/readiness`, `/healthz/startup`; `grpc.health.v1` on the same port (also on every public gRPC server) |
| shutdown | SIGTERM; the SDK stops within `BACKPLANE_SHUTDOWN_TIMEOUT` (25s) — keep the orchestrator's grace period above it; a second signal exits at once |

Nothing but the identity is required: without Consul, NATS, Temporal or a
collector the service starts and serves, logging one warning per missing
dependency. `backplane.RequireNATS()` / `RequireTemporal()` in the code make
a connection part of readiness.

### SDK block: every variable

Durations are Go durations (`30s`, `2m`); lists are JSON (`["a","b"]`);
TLS material is PEM content, not paths. Secrets are masked in logs and in
the instance state.

| variable | default | meaning |
|---|---|---|
| `BACKPLANE_CONSUL_ADDR` | — | Consul agent; empty: no Consul (no registration, no KV layer) |
| `BACKPLANE_CONSUL_TOKEN` | — | ACL token (secret); else `CONSUL_HTTP_TOKEN(_FILE)` |
| `BACKPLANE_CONSUL_DATACENTER` | agent's | datacenter of queries |
| `BACKPLANE_CONSUL_TLS_ENABLED` | `false` | HTTPS to Consul; replaces the `CONSUL_*` TLS variables entirely |
| `BACKPLANE_CONSUL_TLS_CA`, `_TLS_CERT`, `_TLS_KEY`, `_TLS_SERVER_NAME` | — | CA (empty: system pool), client certificate and key (together), server name |
| `BACKPLANE_CONSUL_TLS_INSECURE_SKIP_VERIFY` | `false` | development only |
| `BACKPLANE_CONSUL_REGISTER` | `true` | `false` when the deployment registers the service (consul-k8s, Nomad) |
| `BACKPLANE_CONSUL_TAGS` | — | tags of the catalog registration, JSON list |
| `BACKPLANE_CONSUL_CHECK_INTERVAL`, `_CHECK_TIMEOUT`, `_DEREGISTER_AFTER` | `10s`, `5s`, `1m` | gRPC health check of the registration |
| `BACKPLANE_CONSUL_SESSION_TTL` | `30s` | session of the instance state (clamped to 10s..24h) |
| `BACKPLANE_NATS_URL` | — | NATS; empty: no events |
| `BACKPLANE_NATS_CREDS` | — | content of a `.creds` file (secret) |
| `BACKPLANE_NATS_TLS_ENABLED`, `_TLS_CA`, `_TLS_CERT`, `_TLS_KEY`, `_TLS_SERVER_NAME`, `_TLS_INSECURE_SKIP_VERIFY` | `false`, … | TLS to NATS, as for Consul |
| `BACKPLANE_NATS_PUBLISH_TIMEOUT` | `5s` | bound of a publish whose context has no deadline |
| `BACKPLANE_NATS_MAX_AGE`, `_MAX_BYTES` | `168h`, `0` | retention of the service's event stream `bp_<service>`; `0`: unlimited |
| `BACKPLANE_NATS_REPLICAS` | `1` | replicas of the event and dead-letter streams (1..5) |
| `BACKPLANE_NATS_DEDUP_WINDOW` | `2m` | dedup window of the event stream, at most `max_age` |
| `BACKPLANE_NATS_DLQ_MAX_AGE` | `720h` | retention of dead letters `bp_dlq_<service>`; `0`: unlimited |
| `BACKPLANE_TEMPORAL_ADDR` | — | Temporal frontend; empty: no hooks, activities, workflows |
| `BACKPLANE_TEMPORAL_NS` | `default` | namespace |
| `BACKPLANE_TEMPORAL_TLS_ENABLED`, `_TLS_CA`, `_TLS_CERT`, `_TLS_KEY`, `_TLS_SERVER_NAME`, `_TLS_INSECURE_SKIP_VERIFY` | `false`, … | TLS (mTLS) to Temporal, as for Consul |
| `BACKPLANE_TEMPORAL_API_KEY` | — | Temporal Cloud API key (secret); turns TLS on by itself |
| `BACKPLANE_TEMPORAL_DIAL_TIMEOUT` | `2s` | one connection attempt; later ones run in the background |
| `BACKPLANE_TEMPORAL_HOOK_TIMEOUT` | `30s` | deadline of a hook call nothing else bounds |
| `BACKPLANE_TEMPORAL_WORKER_ENABLED` | `true` | `false`: this replica runs no activity/workflow worker (hook calls still work) |
| `BACKPLANE_TEMPORAL_WORKER_MAX_CONCURRENT_ACTIVITIES`, `_MAX_CONCURRENT_WORKFLOW_TASKS`, `_ACTIVITY_POLLERS`, `_WORKFLOW_POLLERS` | `0` | worker tuning; `0`: Temporal's default |
| `BACKPLANE_SHUTDOWN_TIMEOUT` | `25s` | whole stop budget |
| `BACKPLANE_SHUTDOWN_DRAIN` | `3s` | pause between deregistration and closing the listeners |
| `BACKPLANE_SHUTDOWN_LISTENERS` | `10s` | in-flight requests of the listeners and the internal API |
| `BACKPLANE_SHUTDOWN_RESERVE` | `5s` | kept for components, dependencies, telemetry; `drain + listeners + reserve <= timeout` |
| `BACKPLANE_HEALTH_INTERVAL`, `_HEALTH_TIMEOUT` | `5s`, `3s` | probe evaluation; `timeout <= interval` |
| `BACKPLANE_SERVER_READ_HEADER_TIMEOUT`, `_IDLE_TIMEOUT`, `_MAX_HEADER_BYTES` | `10s`, `2m`, `1048576` | HTTP limits of both port classes |
| `BACKPLANE_SERVER_GRPC_MAX_RECV_MSG_SIZE` | `4194304` | gRPC message limit |
| `BACKPLANE_SERVER_GRPC_KEEPALIVE_MIN_TIME`, `_GRPC_PERMIT_WITHOUT_STREAM` | `30s`, `true` | gRPC keepalive enforcement (Envoy pings) |
| `BACKPLANE_INSTANCE` | `<service>-<hostname>` | instance id = Consul service id |
| `BACKPLANE_ADVERTISE` | `POD_IP`, else hostname's IP | address registered in Consul |
| `BACKPLANE_ENVIRONMENT` | — | `deployment.environment.name` |
| `BACKPLANE_INTERNAL_PORT` | `9400` | platform port |
| `BACKPLANE_INTERNAL_SECRET` | — | secret backplane presents on the platform port (secret); empty: the internal API and bundle are open to whoever reaches the port |
| `BACKPLANE_INTERNAL_SECRET_PREVIOUS` | — | also accepted while the secret rotates |
| `BACKPLANE_PUBLIC_PORT` | `8080` | shared public port of managed routes |
| `BACKPLANE_LOG_LEVEL` | `info` | log level; live from Consul KV `config/<service>/backplane/log_level` |
| `BACKPLANE_PPROF` | `false` | `/debug/pprof/*` on the platform port, behind the secret |
| `BACKPLANE_CONFIG_FILE` | — | configuration file (not a block field) |

The standard Consul client variables (`CONSUL_HTTP_TOKEN`,
`CONSUL_HTTP_TOKEN_FILE`, `CONSUL_HTTP_SSL`, `CONSUL_CACERT`,
`CONSUL_CAPATH`, `CONSUL_CLIENT_CERT`, `CONSUL_CLIENT_KEY`,
`CONSUL_TLS_SERVER_NAME`, `CONSUL_HTTP_SSL_VERIFY`) fill what the block
leaves empty; only `BACKPLANE_CONSUL_ADDR` turns Consul on.

## Platform-in-a-box

`make up` starts `docker-compose.yaml` and waits for it; `make down` stops
it. Ports are published on localhost:

| service | image | ports | notes |
|---|---|---|---|
| Consul | `hashicorp/consul` | 8500 (HTTP), 8600/udp (DNS) | `agent -dev`: in-memory, no ACL |
| NATS | `nats` | 4222, 8222 (monitoring) | JetStream on (`-js`) |
| Temporal | `temporalio/temporal` | 7233 (frontend), 8233 (UI) | **dev server** (`server start-dev`): SQLite in memory, Nexus enabled, namespace `default`; enough for development and conformance — production runs a real Temporal cluster |
| PostgreSQL | `postgres` | 5433 | for backplane (user/password/db `backplane`); `backplane_scratch` is created by `make db-migration` |
| Envoy | `envoyproxy/envoy` | 10000 (traffic), 9901 (admin) | bootstrap `envoy/envoy.yaml`; routes come over xDS from backplane |

Run the example against it: `make run-hello` (builds `bin/hello` and points
it at Consul; add `BACKPLANE_NATS_URL=nats://localhost:4222` and
`BACKPLANE_TEMPORAL_ADDR=localhost:7233` for events, hooks and workflows).

The dev Temporal keeps nothing across restarts: Nexus endpoints, schedules
and workflow history vanish with the container.

## The backplane server

backplane is a service on its own SDK: the whole SDK block above applies
(`BACKPLANE_CONSUL_*`, `BACKPLANE_INTERNAL_PORT`, …), plus its own
variables — the same `BACKPLANE_` prefix, since the service is named
`backplane`:

| variable | default | meaning |
|---|---|---|
| `BACKPLANE_PG_DSN` | — | **required**: PostgreSQL (secret); everything lives in schema `backplane`, created and migrated at start (the role needs `CREATE` on the database) |
| `BACKPLANE_CONSUL_ADDR` | — | **required** for backplane (optional for services): the registry follows the catalog and `backplane/services/` |
| `BACKPLANE_XDS_LISTEN` | `:18000` | xDS (ADS, delta and state-of-the-world) for Envoy; must be reachable from every Envoy |
| `BACKPLANE_XDS_HTTP_PORT` | `10000` | port of Envoy's public HTTP listener the xDS snapshot describes (Envoy binds it, backplane does not) |
| `BACKPLANE_CONSOLE_LISTEN` | `:8081` | console HTTP (`/`, `/ws`, `/auth`, `/plugins`), behind Envoy |
| `BACKPLANE_CONSOLE_HOST` or `BACKPLANE_CONSOLE_PREFIX` | — | the console on its own host, or under a prefix (`/backplane`) of a shared one; not both |
| `BACKPLANE_CONSOLE_ORIGINS` | the request's own host | origins (`scheme://host[:port]`, exact) allowed to open `/ws` and to log in, JSON list |
| `BACKPLANE_CONSOLE_TRUSTED_PROXIES` | — | addresses/CIDRs whose `X-Forwarded-For` is trusted (Envoy's), JSON list |
| `BACKPLANE_CONSOLE_INSECURE_COOKIE` | `false` | `true`: the session cookie without `Secure` and no HSTS — plain-HTTP development only (`make run-backplane` sets it) |
| `BACKPLANE_ADMIN_TOKEN` | — | **required**: the console's admin token (secret), at least 16 characters |
| `BACKPLANE_OBS_METRICS_URL`, `_LOGS_URL`, `_TRACES_URL` | — | observability backends for the console |

Listen ports must differ from each other and from the platform and public
ports. Readiness (`/healthz/readiness` on the platform port) waits for
PostgreSQL (migrated, pinging) and the first registry snapshot from Consul;
later Consul outages keep the last snapshot and readiness.

Locally, against platform-in-a-box, next to services on their default
ports:

```sh
make up
make run-backplane   # bin/backplane: Consul localhost:8500, PostgreSQL localhost:5433,
                     # platform port 9410, public port 8090, console admin token
                     # dev-admin-token-local-change-me (DEV_ADMIN_TOKEN=... to override)
make run-hello       # another terminal: the registry logs hello's manifest and instance
curl localhost:9410/healthz/readiness
```

### Envoy in front, locally

The compose Envoy (`envoy/envoy.yaml`) asks for everything over ADS at
`host.docker.internal:18000` — backplane on the host (`make
run-backplane`). Once backplane has synced with Consul it serves one
snapshot to every Envoy: the public listener on 10000 and every service's
routes; `backplane` logs `xds snapshot` and `envoy connected`.

Envoy runs in a container, so the address a service registers in Consul
(`BACKPLANE_ADVERTISE`) must be reachable from the Envoy (and Consul)
containers. The SDK's default — the IP of the host name — usually is the
host's LAN address and works; when it is a loopback, set the source
address of the default route:

```sh
make up
make run-backplane                                   # xDS on :18000
BACKPLANE_ADVERTISE=$(ip -4 route get 192.0.2.1 | awk '{print $7; exit}') make run-hello
curl 'localhost:10000/hello/?name=x'                 # Hello, x! — through Envoy
curl localhost:9901/config_dump                      # what Envoy got
```

`host.docker.internal` (the bridge gateway, `extra_hosts:
host-gateway`) is how Envoy finds backplane; it is not an address a
service knows as its own, so services advertise the LAN address instead.
A host firewall must let containers reach these ports. Envoy keeps the
last snapshot while backplane is down; `curl
localhost:9901/stats?filter=update_rejected` shows NACKs (backplane logs
each with Envoy's reason).

Cluster names in Envoy (and its stats): `<service>_grpc` / `<service>_http`
at the port registered in Consul, `<service>_p<port>_grpc|http` at a
route's own port (every managed route carries its port), and
`backplane_console`. gRPC and Connect clusters speak HTTP/2 upstream, the
others HTTP/1.1.

### The console

The console (`internal/console`) listens on its own address
(`BACKPLANE_CONSOLE_LISTEN`); Envoy reaches it through the cluster
`backplane_console` at `<host or prefix>/`, WebSocket upgrade on, path
passed as is. Everything is under that base:

| path | what |
|---|---|
| `POST /auth/login` | `{"token": "<admin token>"}` → cookie `bp_session` (`HttpOnly; Secure; SameSite=Strict`); `429` with `Retry-After` when rate-limited |
| `POST /auth/logout`, `GET /auth/session` | end the session; the current session or `401` |
| `GET /ws` | ws-proto: `backplane.console.v1` (`CatalogService`, `SessionService`, `ConfigService`) and the relay to the services' internal API; needs the cookie and an allowed `Origin` |
| `GET /plugins/<service>/<hash>/<path>` | console plugin bundles (with the cookie), fetched from a live instance's platform port and cached by hash |
| `GET /` | the shell (a placeholder until it is built in) |

The admin token is configuration: `BACKPLANE_ADMIN_TOKEN`, a secret of
the deployment shared by every replica. backplane neither generates nor
stores it; without it (or with one shorter than 16 characters) backplane
does not start. To change it, change the secret and roll the replicas;
sessions opened with the old token stay valid until they expire or are
revoked (one by one, or all but the current one from the console).
Sessions last 12 h, end after 1 h without activity, and are listed and
revoked from the console.

What the deployment provides:

- **one secret everywhere**: backplane's `BACKPLANE_INTERNAL_SECRET` is what
  it presents to every service's platform port (relay and bundles); every
  service must accept it (`BACKPLANE_INTERNAL_SECRET`, or `_PREVIOUS` while
  it rotates, §13 of the design);
- **network**: backplane reaches every service's platform port
  (`BACKPLANE_INTERNAL_PORT`) at the address the instance publishes;
- **TLS in front**: the cookie is `Secure` — serve the console over HTTPS
  (Envoy or a load balancer terminates it); `BACKPLANE_CONSOLE_TRUSTED_PROXIES`
  lists Envoy's addresses so the brute-force limit and the session list see
  the real client address;
- **origins**: behind a proxy that rewrites `Host`, list the public origin
  in `BACKPLANE_CONSOLE_ORIGINS` (`["https://console.example.com"]`).

Development of the store (`internal/store`, sqld):

| command | does |
|---|---|
| `make db` | generates `internal/store/db` from `schema.sql` + `queries/`, validates the migrations (also part of `make gen`) |
| `make db-migration name=<what>` | writes `internal/store/migrations/<timestamp>_<what>.sql` from the diff of `schema.sql` against the migration history, realized in the scratch database `backplane_scratch` (`DB_DEV_URL`; sqld drops every schema in it — never point it at a real database) |
| `bin/sqld migrate status -c sqld.yaml --db 'postgres://backplane:backplane@localhost:5433/backplane?search_path=backplane'` | applied / pending / drift of a database (`search_path`: the history table lives in schema `backplane`) |

### The whole stack locally: backplane, hello, Envoy

Everything of M1 on one machine, against platform-in-a-box. Both
processes advertise the host's LAN address (Envoy and Consul run in
containers), share one internal secret (backplane presents it on hello's
platform port: relay and bundles) and the console sits under `/backplane`
of Envoy's `:10000`:

```sh
make up
ADDR=$(ip -4 route get 192.0.2.1 | awk '{print $7; exit}')

# terminal 1: backplane — xDS :18000, console :8081, platform port 9410,
# admin token dev-admin-token-local-change-me (make's DEV_ADMIN_TOKEN)
BACKPLANE_ADVERTISE=$ADDR BACKPLANE_INTERNAL_SECRET=dev-secret \
BACKPLANE_CONSOLE_PREFIX=/backplane make run-backplane

# terminal 2: hello — platform port 9400, public 8080
BACKPLANE_ADVERTISE=$ADDR BACKPLANE_INTERNAL_SECRET=dev-secret make run-hello
```

Then, through Envoy:

```sh
curl 'localhost:10000/hello/?name=x'                  # Hello, x!

# Login: the admin token for a session cookie (bp_session, Path=/backplane;
# without Secure because run-backplane sets BACKPLANE_CONSOLE_INSECURE_COOKIE).
# A browser sends Origin itself; it must be the console's own origin.
curl -c jar -H 'Origin: http://localhost:10000' -H 'Content-Type: application/json' \
  -d '{"token":"dev-admin-token-local-change-me"}' localhost:10000/backplane/auth/login
curl -b jar localhost:10000/backplane/auth/session    # the session, 401 without the cookie

# hello's UI bundle through the console: the hash is the bundle's ETag
# on hello's platform port (behind the secret) and ui.hash of its manifest.
HASH=$(curl -sI -H 'Bp-Internal-Secret: dev-secret' localhost:9400/_backplane/ui/plugin.json \
  | awk -F'"' 'tolower($1) ~ /^etag/ {print $2}')
curl -i -b jar "localhost:10000/backplane/plugins/hello/$HASH/plugin.json"   # 200, immutable

# What the configuration cycle leaves in Consul (after a save from the console):
curl 'localhost:8500/v1/kv/config/hello/?recurse'     # values and _revision
curl localhost:9410/healthz/readiness                 # backplane's readiness
```

Everything else of the console — the catalog, Live configuration
(validate, save, rollback), sessions and the relay to hello's
`AdminService` — is ws-proto on `ws://localhost:10000/backplane/ws` with
the cookie and `Origin: http://localhost:10000`; until the shell is built
in, `conformance/m1_test.go` is the executable walk-through of those
calls (`make test-m1`, below). A revision saved there lands in
`config/hello/` and changes the greeting: `?!` as `greeter.suffix` gives
`Hello, x?!`.

## Conformance

The contract tests in `conformance/` run the SDK from outside, against
platform-in-a-box. Every suite is enabled by its variable and skipped
without it:

| variable | enables |
|---|---|
| `BACKPLANE_TEST_CONSUL=localhost:8500` | manifest and instance state in KV, catalog, platform port and bundle, public protocols, hot reload, graceful stop (builds and runs `examples/hello`); the console's relay to hello's `AdminService` and its bundle through `/plugins/` (`internal/console`: builds and runs its own `hello` without Consul, so it never meets the conformance one) |
| `BACKPLANE_TEST_NATS=localhost:4222` | events end to end, reactors: dead letters and redrive, stop without dead letters, schema evolution |
| `BACKPLANE_TEST_PG=postgres://backplane:backplane@localhost:5433/backplane` | the backplane store: migrations, installation, transactions (`internal/store`); console sessions (`internal/console`, in a scratch database it creates and drops — the role needs `CREATEDB`) |
| `BACKPLANE_TEST_ENVOY=localhost:9901` with `BACKPLANE_TEST_CONSUL` | xDS end to end (`internal/xds`, `make test-envoy`): the control plane on `:18000` against the live catalog, `examples/hello` through the compose Envoy — HTTP, gRPC and a stream, gRPC-Web, REST-JSON, ws-proto, CORS, a route on its own port, the console under a prefix — and no NACK; needs `:18000` free (no `make run-backplane`) and waits for a running conformance `hello` to go |
| `BACKPLANE_TEST_TEMPORAL=localhost:7233` | hooks through a binding (the test plays backplane's Nexus side), hooks from workflows and lifecycle hooks, activities by name, workflows and schedules |
| `BACKPLANE_TEST_ENVOY` with `BACKPLANE_TEST_CONSUL` and `BACKPLANE_TEST_PG` | M1 end to end (`conformance/m1_test.go`, `make test-m1`): the built `backplane` and `hello` behind the compose Envoy — console login and `/ws` through Envoy and directly, the catalog, Live configuration (validation, revisions in Consul KV applied by hello, reconciler repairs, rollback), the relay, the plugin bundle, hello leaving and rejoining Envoy's routes |
| `BACKPLANE_TEST_ENVOY` with `BACKPLANE_TEST_CONSUL`, `BACKPLANE_TEST_PG`, `BACKPLANE_TEST_NATS` and `BACKPLANE_TEST_TEMPORAL` | M2 end to end (`conformance/m2_test.go`, `make test-m2`): the built `backplane` and `hello` with NATS and Temporal behind the compose Envoy — `hello.Greet` unbound (`NoBinding`, local fallback), the binding `hello.Greet := hello.Echo` from its text form (validate, save, served to `GET /hello/` through Envoy, its run and steps), invalid definitions refused, one trace id from the HTTP request through the hook, the binding run and `Echo`, `TestBinding`, delete; the rule `on hello.Greeted := hello.Echo` (one run per matching event `rule/<id>/<ce-id>`, none for a filtered one or a republished `ce-id`, pause and resume), `TestRule` |

```sh
make up
BACKPLANE_TEST_CONSUL=localhost:8500 \
BACKPLANE_TEST_NATS=localhost:4222 \
BACKPLANE_TEST_TEMPORAL=localhost:7233 \
BACKPLANE_TEST_PG=postgres://backplane:backplane@localhost:5433/backplane \
  GOWORK=off go test -race -count=1 ./...
```

`make test-envoy`, `make test-m1` and `make test-m2` need Envoy's ADS
port `:18000` to themselves (no `make run-backplane`, one at a time) and
run alone:

```sh
make up
make test-m1   # BACKPLANE_TEST_{CONSUL,PG,ENVOY} set; -run '^TestM1$' ./conformance/
make test-m2   # BACKPLANE_TEST_{CONSUL,PG,ENVOY,NATS,TEMPORAL} set; -run '^TestM2$' ./conformance/
```

`make test-m1` builds `cmd/backplane` and `examples/hello` and runs both
on free ports next to the compose containers: backplane in a scratch
database it creates on the compose PostgreSQL and drops afterwards (the
role needs `CREATEDB`), with a random admin token and internal secret,
the console under `/backplane`, a 500ms reconcile interval; hello with
the same secret, registered in Consul at the host's LAN address. It waits
for a `hello` of another run to leave Consul first, and removes what it
wrote: `config/hello/`, `backplane/services/hello/`, its backplane
instance and manifest (version `0.0.0-m1`). On failure it prints both
processes' logs. Envoy's ADS reconnect backoff may take up to 30s after
`:18000` was idle.

`make test-m2` runs the same pair with NATS (`BACKPLANE_NATS_URL`) and
Temporal (`BACKPLANE_TEMPORAL_ADDR`) set on both, walks the M2 scenario
through the console's `/ws` via Envoy (bindings, runs, rules, one trace
from `GET /hello/` with a `traceparent` to the `Echo` call) and removes
what it made: the scratch database (bindings and rules), hello's streams
`bp_hello` and `bp_dlq_hello` in NATS (with every consumer on them, the
rule's `backplane__rule-<id>` included; deleted before the run too),
`config/hello/`, `backplane/services/hello/`, its backplane instance and
manifest (version `0.0.0-m2`). The Nexus endpoint `hello` stays in
Temporal (backplane points it at its own queue on every start; an idle
endpoint costs nothing), as do the finished runs. On failure it prints
both processes' logs; every wait names what it waited for and what it
saw last.

The same variables enable the SDK's own integration tests under
`pkg/backplane` and the server's under `internal/` (`BACKPLANE_TEST_CONSUL`:
the registry against a live Consul). Names of services, streams, queues and endpoints are unique
per run, so repeated runs do not collide; the `hello` suite refuses to run
while another `hello` instance is live on the same Consul.

## Telemetry the console reads (backplane)

backplane shows what it can find in the deployment's telemetry stack
(VictoriaMetrics / VictoriaLogs / VictoriaTraces in v0). Scrape into it:

| source | endpoint | labels expected |
|---|---|---|
| services | OTLP from the SDK (`OTEL_EXPORTER_OTLP_ENDPOINT`) | resource `service.name`, `service.instance.id`, `service.version`, `deployment.environment.name` |
| Envoy | `:9901/stats/prometheus` | `envoy_cluster_name` = `<service>_<…>`: the service is the part before the first `_` |
| Temporal server | its Prometheus endpoint | `task_queue` = service name |
| NATS | `prometheus-nats-exporter` against `:8222` | `stream_name` = `bp_<service>` |

Without these the corresponding panels are empty; everything else works.
The SDK's own metrics and spans are listed in `platform-design.md` §15.4.

## Files here

- `envoy/envoy.yaml` — Envoy bootstrap, the minimum: admin on 9901, node
  id and cluster, the `xds` cluster; listeners, routes, clusters and
  endpoints come over xDS (ADS, delta) from backplane on port 18000
  (`host.docker.internal`).
