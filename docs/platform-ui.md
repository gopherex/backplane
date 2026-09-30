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
| `AutomationPanel` | A service's wiring at a glance: its hooks with binding state, version and activities, the rules on its events with state, and the bindings and rules elsewhere that call its activities; every row opens it in Wiring (`onOpenWiring`) |
| `WiringWorkspace` | Wiring (see below): live lists of bindings and rules by service, the activity palette, an overview of what needs attention, and the editor of the open item; state (`target`, `view`) reported through `onStateChange` for the URL |
| `WiringEditor` | One binding or rule: Graph and YAML on the same draft, problems, inspector, versions, test and runs |
| `OperationSelector`, `ServiceOperations` | Declared hooks, activities, events and workflows of a service; schemapb input form when declared, JSON otherwise; outcome with status and timing |
| `WorkflowsPanel`, `WorkflowRuns`, `SchedulesPanel`, `RunDrawer`, `RunTable`, `RunInspector` | Declared workflows with start-from-schema, an optional workflow id and execution timeout (a running id is refused and says so); a warning when no worker polls a workflow's task queue. Runs switch between the service's workflows and its hook calls (`<service>.hooks` and the console's calls), filter by workflow type (declared and seen), status (including continued-as-new) and workflow-id prefix (a removable chip; a schedule's "Runs of this schedule" sets it), page with tokens and refresh after a start or trigger. The run drawer shows status, who started it (memo `source`) and the rest of the memo, parent and continued-as runs as links opening that run in the same drawer (with a way back), input/result (backplane envelopes with their payload as JSON), failure, every field of pending activities, history with expandable payloads and links to child runs, and cancel/terminate/signal (an empty signal payload sends no argument); a running run refreshes every 3 s while shown. Schedules show Temporal's state against the declaration: "Not in Temporal" (actions disabled), drift of the paused flag, next runs (up to 5), recent runs and running ids linking to their runs, missed/skipped counters, created/updated, owner; pause/resume ask for an optional note. `RunDrawer` accepts a `RunSource` so binding and rule runs load and cancel through their own APIs; their terminate and signal go through WorkflowService, as the controls say |
| `EventStreams`, `DeadLettersPanel` | Events the service publishes and consumes; stream state (`GetStream`); per event subscribers with pending/ack/redelivery/dead letters, newest-first message pages, message drawer, test publish from schema; dead letters by consumer with redrive and purge |
| `AuditFeed`, `useAuditFeed` | Filters with value pickers (action, outcome, service, actor, subject, operation), live tail, bounded deduplicated replay, pagination, cursor-expiry state, entry drawer (an entry with a workflow id opens its run); `service` scopes the feed |
| `ExplorePanel`, `ObsResults`, `TraceLookup`, `TraceView` | See below |
| `SystemMap`, `collectWires` | Graph of services and their connections (bindings: hook owner → activity owner; rules: event owner → activity owner; subscriptions: event owner → consumer) with filters per kind and a connection list |

## Wiring

A binding or rule is edited as YAML (its protojson written as YAML: steps
by name, values as JSON trees whose strings are CEL) or as a graph; both
change the same draft text, so switching views loses nothing and the YAML
keeps its comments.

- YAML: syntax and shape errors immediately (unknown keys, durations such as
  `30s`, `1m30s`, `500ms`), server violations from `AnalyzeBinding` /
  `AnalyzeRule` 250 ms after typing stops, underlined at their place (a JSON
  Pointer mapped to the YAML node, an expression range inside the scalar).
  Completion: keys by position, hook/event/activity names from the catalog,
  step names, CEL variables and schema fields after `x.`. The panel beside it
  shows the path, CEL type, expected type, description and reads at the
  cursor.
- Graph: the trigger (hook input or event and meta), one node per step with
  typed input ports (the expression of each field) and output ports, the
  result. Edges come from the analysis: data reads, `when` reads (dashed
  amber) and `after` (dotted). Drag an activity from the palette to add a
  step, drag an output port to an input port to write the reference, drag a
  node to save its position in `editor`, Delete removes a step (and its
  name from every `after`) or unsets an edge that is exactly a reference.
  "Add step…" adds a step without dragging; "Tidy" forgets saved positions
  and lays the graph out by levels; the mini-map is on demand. The inspector
  edits the selected step: rename (server-side, by syntax trees), activity,
  when, input fields with inline CEL and completion, after, undo, retry and
  timeouts; its fields commit on blur, one undo step each.
- Save sends `base_version`; a concurrent save shows a banner with a diff
  against the latest and "continue from" it. Drafts survive switching items;
  leaving the page with drafts asks first. A version opens as a YAML diff
  against the current one, to roll back to or to open as a draft. Deleted
  rules stay in a collapsed group; their versions restore them. A rule needs
  at least one step.
  A broken definition (manifests changed) is flagged in the lists, the
  overview and the editor.
- Test runs the draft (unsaved) or the saved version; its run's step status
  and timing overlay the graph, as does any run opened from Runs.

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
