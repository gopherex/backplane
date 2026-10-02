# Investigate durable audit

Audit combines platform control entries and trusted application audit records in a PostgreSQL-backed feed. It remains available when NATS or telemetry storage is disabled.

## Platform operations

Database mutations such as configuration or binding saves commit their audit entry in the same transaction. External commands such as starting a workflow first persist an intent, then dispatch, then persist the result. There is no distributed transaction with Temporal or NATS.

| Outcome | Interpretation |
| --- | --- |
| `succeeded` | The API confirmed completion or acceptance; a started workflow may still fail later |
| `rejected` | Input, permission or precondition prevented the command |
| `failed` | The returned operation result reports failure |
| `partial` | A batch contains both completed and failed items |
| `unknown` | Transport/cancellation/deadline left the outcome ambiguous |

An intent with no result is also unknown. Search by operation ID, inspect the target run/event/schedule and decide whether another command is appropriate. Automatic retry can duplicate effects.

Control audit stores allowlisted metadata, not payloads, configuration values or credentials. Optional export sends OTel Logs through a durable outbox with stable entry IDs. It does not publish `AuditEntry` into NATS.

## Application audit through OTel

An application emits an ordinary log with boolean `backplane.audit=true`:

```go
log.Info("identity deleted", backplane.Audit(),
    xlog.String("event.name", "identity.deleted"),
    xlog.String(backplane.AuditSubject, "identity/"+id),
    xlog.String(backplane.AuditOutcome, "succeeded"))
```

The trusted Collector pipeline forwards marked records to Backplane's internal audit OTLP/gRPC listener. It commits before acknowledging and deduplicates retries. Third-party logs can receive the label in the Collector without changing the application.

Public browser OTLP is marked by the proxy and excluded from this audit pipeline. Exposing the internal audit listener publicly would bypass that trust boundary. Preserve the supplied pipeline separation and restrict direct Collector access to trusted senders.

## Read the feed

Use text, time range and field/attribute filters. The histogram supports narrowing a period; facets expose frequent values. Service views pin the relevant service, while installation-wide session entries have no service.

`SearchAudit` returns newest-first pages with opaque filter-bound cursors. Continue older pages using the same filter. A live view refreshes the first page; there is no `WatchAudit` replay stream. Keep exact IDs/timestamps and distinguish platform/application sources.

Application audit has no platform expiry. Control-audit retention is deployment-configured and defaults to retaining everything. Optional export outage can delay cleanup of pending records. See [complete persistence, ingest and API reference](../reference/audit.md).
