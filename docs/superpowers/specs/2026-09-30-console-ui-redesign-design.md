# Console UI redesign — design

Date: 2026-09-30. Status: implemented (all stages, including the system map). Desktop only: narrow-screen layouts are out of scope.

## Intent

The operator console works functionally but reads as a prototype. Goal: a
professional operator console on the level of Grafana, HyperDX, SigNoz, GitLab
and the owner's own products — Stroppy Cloud (`stroppy-io/stroppy-cloud/web`)
and Komeet (`naukograd-software/komeet`, admin panel and app). Every surface —
shell, platform pages, module pages — must look like one product.

Stated by the user:

- current UI/UX is unacceptable; make it look professional;
- the UI kit stays ours (`@gopherex/backplane-ui`, shadcn/radix/tailwind); all
  surfaces must look uniform;
- Services must support rich cards and a table, switchable;
- a system map (graph of services and their wiring) comes after the main
  screens;
- work directly on `master`.

Assumed (carried over from the accepted information architecture):

- header + left navigation tree: Services (landing), Explore, Audit, then
  service branches whose leaves are module-owned pages;
- dark and light themes, English only, operator-token cookie login;
- module SDK/runtime boundaries (`/services/:service/...` platform,
  `/s/:service/*` module, host-owned router/theme/i18n/socket, descriptor/CSP
  checks) are unchanged.

Success: every screen is composed from shared kit compositions, reads cleanly
at 1440px and 390px in both themes, shows real data in human form (no raw
nanoseconds, booleans or unlabeled inputs), and covers loading, empty, error,
session-loss and large-data states.

## Findings (current state)

- Pages have no hierarchy: no panels, stat tiles or entity headers; inputs have
  no labels; primary buttons span the full width.
- Raw values are rendered: epoch nanoseconds in Explore, `true` for health,
  configuration as bordered fieldsets with checkboxes, Operations as a bare JSON
  editor.
- Bindings and rules have a complete API (list, get, versions, validate, test,
  runs, rollback, pause/resume) and no screens. Sessions (`ListSessions`,
  `RevokeSession`) have no UI.
- The manifest (nodes, routes, hooks, activities, events, subscriptions,
  workflows, schedules, config schema) and instance state (phase, uptime,
  applied/rejected revision, node readiness, transports, per-key config source)
  are almost entirely unused.
- The hello module renders unstyled text and buttons.
- `platform-ui` has no page-level compositions; the kit has primitives
  (Toolbar, StateMessage, DataTable, ConfirmAction, SearchInput, Segment,
  FilterPill) but no Panel, PageHeader, StatTile, StatusBadge, KeyValue,
  Timestamp.
- `docs/ui-handoff.md` is partly outdated; it will be rewritten as current state.

## Approach

Foundation first. Tokens and density, then new page-level compositions in
`@gopherex/backplane-ui` (each with Storybook stories in both themes), then
the shell, login and Services. After that, a visual checkpoint with the user.
Only after approval propagate to the remaining screens and the module
template. Rejected: restyling pages with console-local CSS (surfaces diverge
again); full static mockups before code (double work).

## Visual language

Material from Grafana/HyperDX, page structure from GitLab/Stroppy, density
from the Komeet admin panel ("reads like an IDE").

- Type: IBM Plex Sans 13px body, 12px secondary, 11px caps group labels;
  IBM Plex Mono with `tabular-nums` for ids, versions, numbers, durations.
- Density: control height 32px (compact 28px), table row 32px, radius 2–4px,
  1px borders, 16/24px page gutters. No cards nested in cards.
- Colour (HyperDX-derived, existing `palette`): dark surfaces `#101113` page,
  `#141517` chrome, `#1a1b1e` panel, `#25262b` raised; green accent; light theme
  neutral GitLab-like greys. Colour carries meaning only: health, run status,
  applied/rejected, diff, severity.
- Status: one `StatusBadge`/`StatusDot` vocabulary for health, phase, run
  status, rule state and applied/rejected.
- Time: relative by default, absolute in a tooltip, nanosecond values kept as
  bigint end to end.
- Motion: none beyond 120ms hover/expand transitions; respects reduced motion.

Tokens live in `packages/theme/src/tokens.ts`; the Grafana theme adapter keeps
charts, time picker and logs in the same palette.

## Kit additions (`@gopherex/backplane-ui`)

New compositions, each with stories covering states and both themes:

| Composition | Purpose |
| --- | --- |
| `PageHeader` | Title, description, primary/secondary actions, optional meta line |
| `EntityHeader` | Name, status badge, meta chips, action group, tab bar slot |
| `Panel` | Titled section: header (title, count, actions, link), body, footer; flush variant for tables |
| `StatTile` / `StatRow` | Metric label, value, delta/secondary line, tone |
| `StatusBadge` / `StatusDot` | Shared status vocabulary with tones |
| `KeyValueList` | Metadata sidebar / detail lists, copyable values |
| `MetaChips` | Inline meta line (version, sdk, instances, uptime) |
| `FilterBar` | Search, filter pills, time range; URL-synchronised through a callback |
| `ViewToggle` | Cards ⇄ table switch (Segment-based) |
| `EmptyState` / `ErrorState` | Consistent empty and failure blocks with actions |
| `DetailDrawer` | Right-side sheet for entry/run/log details |
| `Timestamp` / `Duration` | Relative/absolute time, ns-safe |
| `JsonViewer` | Collapsible read-only JSON with copy |
| `SplitPane` | Resizable list/detail layout (Explore, Audit) |
| `EntityCard` | Card for list entities: header, status, stats, footer links |

Existing primitives (Button, Input, Table, DataTable, Tabs, Toolbar,
StateMessage…) are restyled to the new density rather than duplicated.
`platform-ui` compositions are rebuilt on top of these.

## Shell

- Header (44px): brand mark, breadcrumbs (`Services / hello / Configuration`),
  command palette (⌘K: services, service tabs, module pages, Explore/Audit),
  connection indicator, theme toggle, session menu (current session, other
  sessions with revoke, log out).
- Sidebar: behaviour kept (pin, rail, temporary expansion, resize, mobile
  drawer, keyboard). Visual: caps group labels, active item with left accent
  bar, health dot per service, module page leaves, admin gear on hover.
- Login: centred card with brand, token field with show/hide, connection and
  failure states. Token never prefilled or stored.
- Global banners for reconnecting / stale catalog / session loss.

## Screens

1. **Services** (landing)
   - `StatRow`: services, instances, healthy, degraded/down, bindings, rules.
   - `ViewToggle` cards ⇄ table, choice persisted per viewer.
   - Card:
     - name, health, latest version, `healthy/total` instance bar;
     - phase summary, SDK version;
     - contract counts: routes, hooks, events, workflows;
     - config applied/rejected;
     - transports health;
     - links to admin tabs and module pages.
   - Table: the same data as columns, sortable.
   - Data source: see Open decisions.
2. **Service** — `EntityHeader` with tabs Overview · Configuration ·
   Automation · Events · Workflows · Telemetry · Audit.
   - **Overview**:
     - instances table: phase, uptime, address, applied revision,
       rejected revision with reason, transports, node readiness in a
       drawer;
     - contract panels: routes, hooks, activities, events, subscriptions,
       workflows, schedules;
     - component tree (manifest nodes);
     - metadata sidebar.
   - **Configuration**:
     - schema form with labels, descriptions, grouping, live-key badge,
       per-key source (default/file/env/kv);
     - draft vs current diff, save;
     - revision list with compare and rollback;
     - per-instance applied/rejected;
     - draft survives reconnect.
   - **Automation**:
     - bindings per hook: current definition, versions, validate/test,
       runs, rollback, delete;
     - rules per event: pause/resume, versions, test, runs;
     - Operations: call hook / run activity with schema-form input and a
       result viewer.
   - **Events**:
     - published streams with stats, peek messages, publish test event;
     - dead letters with redrive and purge (confirmed).
   - **Workflows**:
     - runs with status filter; run drawer with history, input, result,
       cancel/terminate/signal;
     - start workflow;
     - schedules with pause/unpause/trigger.
   - **Telemetry**: Explore scoped to the service's selectors.
   - **Audit**: Audit scoped to the service.
3. **Explore** — Grafana Explore / HyperDX layout:
   - top bar: signal (Logs/Metrics/Traces), source, query language, time
     range, run;
   - field sidebar (fields/values, click to filter);
   - results:
     - log volume histogram and virtualised log table;
     - row detail with attributes and trace link;
     - trace list and waterfall;
     - metric charts;
   - query, source, signal and time range persist in the URL.
4. **Audit**:
   - FilterBar: actor, action, subject, outcome, service, time;
   - dense table; entry drawer with payload;
   - live tail;
   - keeps reconnect and cursor-expiry semantics.
5. **Module template and hello**: built from the same compositions so module
   pages look native; the template becomes the reference for future modules
   (e.g. the Komeet admin panel).
6. **System map** (stage 8): `@xyflow/react` graph.
   - Nodes: services.
   - Edges: event → subscription/rule and hook → binding.
   - Node click opens the service. Filter by service.

## States and accessibility

Every screen has loading (skeleton), empty, error with retry, permission or
session failure, partial results, and large-dataset behaviour (virtualisation
or pagination). Keyboard: full focus order, visible focus ring, `Esc` closes
drawers, ⌘K palette. Mobile (≤760px): sidebar drawer, tables scroll inside
panels, cards single-column.

## Stages

1. Foundation (tokens, kit compositions, stories), shell, login, Services
   (cards + table) → **visual checkpoint with the user**.
2. Service Overview and Configuration.
3. Automation (bindings, rules, operations), Events, Workflows.
4. Explore.
5. Audit and scoped Telemetry/Audit tabs.
6. Module template and hello.
7. States, mobile and accessibility pass across all screens; docs rewrite.
8. System map.

## Verification

- Screenshots of every screen at 1440×900 and 390×844, dark and light, taken
  through the browser against the live dev stack.
- `make web-check` (builds, typecheck, unit, browser, Storybook, Go console
  test) and `make test-dev`; browser scenarios updated for new markup.
- Storybook stories for every new composition; the `/backplane/dev` page stays
  as the technical acceptance page.
- `docs/ui-handoff.md`, `docs/ui-components.md`, `docs/platform-ui.md`
  rewritten as current state.

## Decisions

- **Services data** comes from `GetService` per service on the client, keyed by
  the catalog index; bindings/rules counts from `ListBindings` and `ListRules`.
- **Service audit** needed an API change: `AuditFilter.service` and
  `AuditEntry.service`, computed when an entry is written (stored, indexed,
  backfilled by the migration), so entries whose subject does not name the
  service (hook calls, rule ids) are included.
- **Layout:** pages fill the window; panels scroll their own content.

## Out of scope

ErrOtel/error grouping, Kratos wrappers, other application modules, deployment
settings, telemetry storage management, account/password flows, publishing
packages, CI and production packaging.
