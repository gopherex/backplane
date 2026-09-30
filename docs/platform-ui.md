# Platform compositions

`@gopherex/backplane-platform-ui` connects the kit to the generated console APIs.
It consumes `BackplaneProvider`; it does not create another connection, router
or console shell. English resources are exported as `platformEnglish` for
`backplane.platform`. Components accept the host's dark/light mode and are
router-agnostic: navigation leaves them through callbacks (`onNavigate`,
`onOpenService`, `onStateChange`).

| Export | Behavior |
| --- | --- |
| `ServiceCatalog`, `ServiceInspector` | Instances (phase, uptime, address, applied/rejected revision, readiness) with an instance drawer (transports, node readiness, per-key configuration source, effective masked configuration); contract panels for routes, hooks with binding state, activities, published/consumed events, workflows, schedules; component tree; metadata |
| `ConfigurationPanel` | Live settings grouped by top-level key; each path shows title, description, unit, per-instance source and effective value; an override switch opens a typed editor (switch, number, text, choice, JSON); dirty markers, validation per path, save with comment; rollout per instance; revision history with a diff drawer and rollback |
| `AutomationPanel` | Live `WatchBindings`/`WatchRules` lists of the service's hooks and of rules on its events; per binding/rule: program (trigger → steps → result), text source, versions with diff and rollback, test runs (the result's workflow id opens its run), paged run list with refresh and step timeline; DSL editor with live parse, validate and save; rules also pause/resume/delete and creation |
| `BindingEditor`, `RuleEditor` | One binding (by hook) or rule (by id, or a new one for an event) with the same views |
| `OperationSelector`, `ServiceOperations` | Declared hooks, activities, events and workflows of a service; schemapb input form when declared, JSON otherwise; outcome with status and timing |
| `WorkflowsPanel`, `WorkflowRuns`, `SchedulesPanel`, `RunDrawer`, `RunTable`, `RunInspector` | Declared workflows with start-from-schema, an optional workflow id and execution timeout (a running id is refused and says so); a warning when no worker polls a workflow's task queue. Runs switch between the service's workflows and its hook calls (`<service>.hooks` and the console's calls), filter by workflow type (declared and seen), status (including continued-as-new) and workflow-id prefix (a removable chip; a schedule's "Runs of this schedule" sets it), page with tokens and refresh after a start or trigger. The run drawer shows status, who started it (memo `source`) and the rest of the memo, parent and continued-as runs as links opening that run in the same drawer (with a way back), input/result (backplane envelopes with their payload as JSON), failure, every field of pending activities, history with expandable payloads and links to child runs, and cancel/terminate/signal (an empty signal payload sends no argument); a running run refreshes every 3 s while shown. Schedules show Temporal's state against the declaration: "Not in Temporal" (actions disabled), drift of the paused flag, next runs (up to 5), recent runs and running ids linking to their runs, missed/skipped counters, created/updated, owner; pause/resume ask for an optional note. `RunDrawer` accepts a `RunSource` so binding and rule runs load and cancel through their own APIs; their terminate and signal go through WorkflowService, as the controls say |
| `EventStreams`, `DeadLettersPanel` | Events the service publishes and consumes; stream state (`GetStream`); per event subscribers with pending/ack/redelivery/dead letters, newest-first message pages, message drawer, test publish from schema; dead letters by consumer with redrive and purge |
| `AuditFeed`, `useAuditFeed` | Filters with value pickers (action, outcome, service, actor, subject, operation), live tail, bounded deduplicated replay, pagination, cursor-expiry state, entry drawer (an entry with a workflow id opens its run); `service` scopes the feed |
| `ExplorePanel`, `ObsResults`, `TraceLookup`, `TraceView` | See below |
| `SystemMap`, `collectWires` | Graph of services and their connections (bindings: hook owner → activity owner; rules: event owner → activity owner; subscriptions: event owner → consumer) with filters per kind and a connection list |

## Layout contract

Compositions fill the height of their container and scroll inside their panels:
a page header or filter bar stays in place, lists and tables scroll with sticky
headers, panel footers (save bars, paging) stay visible. Give the component a
bounded-height flex or grid parent; without one it grows with its content.

## Explore

Explore is a query builder over the native languages the deployment advertises.
Filters with known values are pickers (service from stored sources, level,
metric names, span names, group-by labels); each change rewrites the native
expression, which stays editable. Field values from the field browser add a
filter. State (signal, query, language, range, limit, open trace) is reported
through `onStateChange` so the console keeps it in the URL.

- Logs: level-stacked volume histogram from LogsQL statistics over the window,
  newest-first lines with level and service, wrap and highlight, live tail,
  loading older lines up to the window start, line drawer with attributes and a
  link to its trace.
- Metrics dashboard: until a metric is chosen or a query typed, Explore shows a
  generated dashboard of every stored metric (scoped to the service when one is
  selected), grouped by family — the service's own metrics, HTTP/RPC,
  Backplane, runtime and the rest. Histograms (`_bucket/_count/_sum`) collapse
  into one p95 panel, series that only grow are shown as their rate, others as
  a sum by service. Panels load when scrolled into view (four queries at a
  time); a panel opens its metric in the builder. The service Telemetry tab
  opens on this dashboard.
- Metrics: metric picker, aggregation (raw, rate, sum, p50/p95/p99 for
  histograms) and group-by; chart in the local time zone and a legend table
  with last/min/avg/max per series that toggles series.
- Traces: spans are grouped into traces (root span, services, span count,
  errors, duration). The trace view is a collapsible span tree on a shared time
  ruler, colored by service, with span search, expand/collapse all and a span
  panel (status, timing, attributes, resource, events, links, related logs).

Query bounds use exact nanoseconds. Tempo JSON uses `lossless-json`; OTLP base64
IDs become hex for correlation; the raw response stays available.

## Runtime behavior

`usePlatformQuery` cancels old requests on key, provider, session or connection
replacement and never shows data of an old session. Read failures keep
same-session data for an explicit retry. `usePlatformAction` suppresses duplicate
in-flight submission and never retries mutations; a failure keeps the draft and
states that the outcome must be checked before repeating. Destructive or
installation-changing actions go through a confirmation dialog.

Audit saves every returned cursor, including empty batches, resumes strictly
after it and deduplicates by entry ID; an expired cursor shows a gap and an
explicit reload. Views keep 1,000 entries by default and report omitted ones.

The catalog's `PlatformFixture` is an explicit local transport covering every
composition, storage failure, no mutation retry, cursor advancement/expiry,
session replacement, schema input and integers above 2^53. Live acceptance runs
against the dev installation; see [development](development.md).
