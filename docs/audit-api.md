# Durable control audit

`backplane.console.v1.AuditService` uses the console cookie and ws-proto transport.
It records platform control operations, not telemetry, arbitrary relay traffic,
reads, watches or automatic application workflow steps. Execution details remain
in Temporal and event payloads remain in JetStream.

## Persistence and coverage

Configuration save/rollback, binding save/delete/rollback, rule save/delete/
rollback/pause and session create/revoke/revoke-others/expiry append an entry and
outbox row in the same PostgreSQL transaction as the mutation. Rejected database
mutations create no success entry. Session touch is not a control mutation.

External commands persist `intent` before dispatch: test event publication,
dead-letter redrive/purge, workflow start/cancel/terminate/signal, schedule
pause/resume/trigger, hook calls, activity runs, binding/rule test and run cancel.
The result has the same `operation_id` and one of these outcomes:

| Outcome | Meaning |
| --- | --- |
| `succeeded` | The API confirmed completion or acceptance of the requested command. Starting a workflow does not mean the workflow completed. |
| `rejected` | Invalid input, failed precondition, missing subject, permission refusal or structured validation violations. |
| `failed` | A returned call result contains a failure, rule evaluation failed, or every attempted batch item failed. |
| `partial` | A dead-letter batch contains both completed and failed items. |
| `unknown` | Dispatch returned an ambiguous transport/backend failure, cancellation or deadline. |

Intent without a result also means unknown. There is no distributed transaction
with Temporal/NATS. Failure to persist intent returns `Unavailable` and does not
dispatch. Failure to persist the result returns `Aborted` with the operation ID;
the command may already have completed. Never automatically replay it. Consult
the operation audit and the external run/event state.

Metadata is allowlisted: actor/session UUID, operation/entry UUID, action,
subject identifiers, revision/rollback reference, changed configuration key
names, pause flag, fixed diagnostic code, workflow/run IDs and affected session
count. Request/output payloads, config values, comments, credentials, tokens and
arbitrary error messages/types are excluded. In particular, `TestRule.event` is
payload and is never copied to the subject. Identifiers are caller-visible
metadata; authors should not put secrets into names or run IDs.

## Ordering and delivery

Updating the singleton audit clock locks it until the mutation transaction
commits. This intentionally serializes audit sequence allocation across replicas:
a higher visible sequence cannot overtake a lower uncommitted entry. Rollback
removes the mutation, entry and outbox together. Sequence allocation is part of
the existing serializable transaction and its serialization retry.

Workers claim at most 16 outbox records with PostgreSQL `SKIP LOCKED`, a unique
lease and 60-second lease timeout. Publish is bounded to 3 seconds per record;
failed records become available after 2 seconds. All claimed records receive an
attempt even if one fails. A crashed worker's lease expires; a stale worker
cannot acknowledge a newer claim. Delivery is at least once, can be reordered
across replicas, and uses entry UUID as the stable CloudEvent/deduplication ID.
Consumers deduplicate by ID and use sequence when ordering matters.

`backplane.AuditEntry` is a normal SDK event. Its sequence is a decimal string;
the RPC uses protobuf `uint64` (`bigint` in TypeScript). The event contains the
same allowlisted metadata as the stored entry. NATS being unavailable never
removes pending records or rolls back a committed control mutation.

## Methods

`ListAudit(filter?, page_size?, page_cursor?)` returns newest first. Default page
size is 100, maximum 500. Filters are exact actor, action, subject, outcome,
operation UUID and inclusive-start/exclusive-end timestamps. Each text filter
is limited to 2048 bytes. Its first response supplies a committed snapshot and
`watch_cursor`. Continue history with `next_page_cursor` and the identical filter;
later commits do not shift that snapshot. Empty next cursor means no older
retained matching entries.

`WatchAudit(filter?, after_cursor)` resumes strictly after a saved watch cursor,
oldest first, in batches of at most 100. The cursor is mandatory. Save the cursor
even on empty heartbeat batches: they advance past nonmatching entries. Entries
are not coalesced; no unbounded per-subscriber queue is maintained. Polling is
every 500 ms, with immediate continuation for full batches. Cancel/unmount closes
the stream. Reconnect with the last processed cursor; deduplicate by entry ID if
the UI received data before persisting its cursor.

Cursors are opaque, versioned and bound to installation, filter and stream/page
kind. They are pagination state, not authorization credentials. All reads require
the same authenticated console session independently of the cursor.

| Code | Handling |
| --- | --- |
| `InvalidArgument` | Correct the filter, UUID, range, limit or mismatched/malformed cursor. |
| `OutOfRange` | Cursor fell outside retained history; refetch the first page and restart the watch. |
| `Unavailable` | Retry the read after the database recovers; preserve the last processed cursor. |
| `Canceled` / `DeadlineExceeded` | Stop or reconnect according to the caller's lifecycle. |
| `Internal` | Stored metadata cannot be decoded; show the failure, do not treat it as empty history. |

Session authentication/revocation follows the common console contract.

## Deployment retention

`BACKPLANE_AUDIT_RETENTION=0s` (default) retains everything. A positive duration
enables hourly cleanup, at most 5000 entries per pass. Only a contiguous delivered
prefix older than the cutoff is removed; an undelivered entry blocks removal of
later entries. Entries, outbox and cursor floor change in one transaction.
Retention is neither editable nor displayed in UI.

## Acceptance

`make test-audit`: rollback atomicity, commit ordering across replicas, durable
intent before dispatch, metadata exclusion/outcome handling, failed publisher
recovery, expired lease takeover, stale acknowledgments, consistent pages,
resume/filter validation and actual retention/cursor expiry against PostgreSQL.

`make test-replicas`: real backplane processes, crash/rejoin, configuration,
bindings/rules/sessions/calls and matching `AuditEntry` events read from NATS
through the console event API. `make test-m1` and `make test-m2` cover existing
control workflows with transactional audit enabled.
