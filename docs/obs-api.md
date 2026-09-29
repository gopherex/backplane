# Observability read API

`backplane.console.v1.ObsService` reads independent Victoria storage. It owns no
Collector, exporters, retention or ingestion transformations. Modules and external
applications can emit telemetry without Backplane. The
[OTLP proxy](telemetry-admission.md) is a separate integration.

## Access and deployment

Operators call the service on the console's existing cookie-authenticated `/ws`.
Trusted servers use gRPC on Backplane's platform port with
`bp-internal-secret: <BACKPLANE_INTERNAL_SECRET>` metadata. The SDK also accepts
previous installation secrets during rotation. If the current secret is empty,
ObsService is **not registered on the internal listener**; cookie reads remain
available. Public ingest keys never authorize reads.

`BACKPLANE_OBS_METRICS_URL`, `BACKPLANE_OBS_LOGS_URL`, and
`BACKPLANE_OBS_TRACES_URL` are API roots. Empty disables that signal. A deployment
may include a path prefix, for example `/select/0/prometheus` for metrics. URLs
must not contain credentials, query parameters or fragments. The optional
`BACKPLANE_OBS_{METRICS,LOGS,TRACES}_AUTHORIZATION` values are secret Authorization
headers owned by deployment. Caller headers, cookies, baggage and credentials
are not forwarded. Redirects are not followed. There are no write endpoints or
arbitrary upstream URL parameters in ObsService.

The platform starts without contacting these stores. Their outages fail queries,
not platform readiness. Query timeout, concurrent requests, response bytes,
expression bytes, range, rows/series/traces and total returned metric points have
deployment budgets under `BACKPLANE_OBS_*`. Defaults: 30 seconds, 8 concurrent,
8 MiB response, 32 KiB expression, 31 days, 200 default / 2000 maximum results,
20000 points. Admission is per replica and rejects immediately when full.

These are request/response bounds, not a replacement for storage execution
budgets. Native query subexpressions can have backend-specific time semantics.
Configure Victoria's search concurrency, memory, scanned-series and time-range
budgets in deployment as well. Arbitrary language options remain backend syntax;
the driver does not silently rewrite them.

## Methods

All methods are unary and read-only; retry is safe. Canceling an in-flight RPC
cancels its HTTP query. There is no pagination cursor: narrow the query or time
range if a result is truncated. Endpoints return complete responses only within
the configured byte budget; oversized JSON is never cut and returned as success.

| Method | Inputs and behavior |
| --- | --- |
| `GetObsCapabilities` | Enabled signals, accepted languages, actual source field mappings, trace lookup and query limits. Does not probe storage health or reveal endpoints. |
| `QueryObs` | Signal, language, expression, explicit nonnegative Unix-nanosecond range (`start < end`), limit, optional step. Zero limit uses the default; excessive values are rejected. |
| `GetTrace` | Nonzero 32-hex W3C trace ID. Returns complete backend Tempo v2 JSON, preserving spans, attributes, events, links and backend extensions. Empty resource-spans response is `NotFound`. |
| `ListObsSources` | Signal/range/limit. Discovers source groups from stored data, independently of modules. Metrics deduplicate a bounded series result; a short source list can still be truncated. |
| `ListObsFields` | Signal/range/limit plus optional native filter. Logs/traces use LogsQL; metrics use a series selector. Empty filter searches all sources in the range. |
| `ListObsFieldValues` | Same plus a nonempty native field name. Bounded suggestion set, never a page. |
| `GetObsSelectors` | Module/service name. Returns deployment override, otherwise current manifest declaration, otherwise exact `service.name` convention. No backend query or admission rule. |

Logs and raw spans support LogsQL. `statistics=true` selects the native
`stats_query` endpoint; positive `step_nanos` selects `stats_query_range`. The
expression must contain a statistics pipe supported by that backend.
Metrics support MetricsQL and its compatible PromQL syntax. Zero step means an
instant query at the range end; positive step means a range query. `statistics`
is invalid for metrics and TraceQL. No signal/language conversion is performed.

TraceQL is opt-in (`BACKPLANE_OBS_TRACEQL=true`). The tested profile is
`victoriatraces-0.12-basic-search`: resource attribute equality, conjunction and
span-duration comparisons in search expressions. It is not full Tempo parity,
nor a TraceQL metrics promise. Other expressions are passed to the backend and
may be rejected. The profile is an acceptance baseline for the pinned
VictoriaTraces v0.12.0 image, not runtime version discovery.

## Data and precision

| Data | Representation |
| --- | --- |
| Logs/raw spans | Native `map<string,string>` rows, including flattened structured bodies. Original missing types are not reconstructed. |
| Metric series | Native labels and samples. Timestamp seconds and values are strings, including decimal precision and `NaN`, `+Inf`, `-Inf`. |
| Trace search | Typed summaries with exact numeric text; original per-trace JSON retained, including span sets. |
| Trace lookup | UTF-8 Tempo JSON bytes. Use a lossless JSON parser; ordinary JavaScript `JSON.parse` may round integer literals. Protobuf JSON trace/span IDs are base64. |

Source grouping uses `service.namespace`, `service.name`, `service.instance.id`.
For the pinned pipeline, logs and metrics use those field names unchanged;
traces use `resource_attr:` before each. Missing/empty grouping fields stay
omitted. No identity is guessed from IP, module registration or a shared
namespace. These fields are untrusted telemetry data, not access controls.

`info.truncated` reports a bounded subset; `info.partial` reports an explicit
backend partial response (HTTP 206 or `isPartial`). Backend warnings are retained.
No flag proves ingestion completeness, excludes late spans or overrides storage
retention. The driver requests full cluster responses by default. Native
LogsQL options can override backend partial-response policy; such language
semantics belong to the backend, not an inferred completeness guarantee.

## Optional declarations

An SDK author can declare related source presets:

```go
svc.TelemetrySources(
    map[string]string{"service.namespace": "identity", "service.name": "wrapper"},
    map[string]string{"service.namespace": "identity", "service.name": "kratos"},
)
```

Selectors are OR alternatives with AND exact resource matches inside each map.
`svc.TelemetrySources()` explicitly disables presets. Omitting the call leaves
convention matching. Deployment `obs.source_overrides.<service>.selectors`
overrides declarations; an empty entry is intentionally different from absence.
Presets prefill module views and never restrict global discovery or ingestion.

## Errors

| gRPC code | Meaning |
| --- | --- |
| `InvalidArgument` | Invalid range/limit/step/ID, unsupported signal/language pair, oversized expression or backend syntax rejection (400/422). |
| `FailedPrecondition` | Signal backend absent or TraceQL disabled. |
| `NotFound` | Trace absent or backend read resource missing. |
| `ResourceExhausted` | No concurrent slot, byte/point budget exceeded, backend 429. |
| `DeadlineExceeded` | Local query deadline or backend 408/504. |
| `Canceled` | Caller canceled. |
| `Unavailable` | Network/backend/authentication failure, redirect or malformed response. Raw backend errors are not echoed because they can contain deployment secrets. |

Console session checks and internal-secret checks run before these methods.
Storage outages never become an empty successful result and do not sign out the
operator. An empty successful response means no matching data in that query.

## Acceptance

`make test-otlp` exercises direct/proxied OTLP and reads the newly stored signals
through ObsService: structured logs, integer above 2^53, nanosecond time, instant
and range metrics, log statistics, native fields/values, undeclared sources,
TraceQL and a cross-service trace. Unit/transport tests exercise budgets,
cancellation, saturation, malformed responses, redirects, credentials, selectors,
cookie revocation and internal-secret access.

Backend contracts: [VictoriaLogs HTTP API](https://docs.victoriametrics.com/victorialogs/querying/),
[VictoriaTraces querying](https://docs.victoriametrics.com/victoriatraces/querying/),
[VictoriaMetrics API](https://docs.victoriametrics.com/victoriametrics/url-examples/).
