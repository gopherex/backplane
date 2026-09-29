# Base UI kit

`@gopherex/backplane-ui` supplies 47 shadcn/Radix primitive modules and platform
compositions. Import its `style.css` alongside `@gopherex/backplane-theme/style.css`.
Register `englishResources` under the `backplane.ui` i18next namespace. Both
themes use the same semantic CSS tokens and focus behavior.

## Composition APIs

| Family | Public API | Contract |
| --- | --- | --- |
| Layout/text | Stack, Grid, Text, TextLink, Toolbar, IconButton | Semantic HTML, token colors, keyboard toolbar movement |
| Actions | ConfirmAction, ClipboardButton | Abortable confirmation, retained failure state, clipboard success/failure |
| Text | SearchInput, SecretInput, AutoSizeInput, AutoSizeTextarea, AutoSaveInput | Clear/reveal, masked readonly secrets, size limits, blur-save with explicit retry |
| Numbers | NumberInput, stepDecimal | Controlled decimal strings and exact bigint arithmetic; units, bounds, readonly |
| Selection | Combobox, TagsInput, Cascader | Static/async and multiple selection; literal string IDs; cancellation and retry |
| Forms | FormControl plus Field components | React Hook Form Controller, field labels/descriptions/errors and native RHF rules |
| Time | DateTimeInput, TimeRangeControl, RefreshControl | ISO calendar/time controls; relative/absolute ranges, timezone, week start; no overlapping refreshes |
| Files | FileUpload | Type/size/count limits, drag/drop, progress, retry and cancellation; caller-owned upload transport |
| Feedback | StateMessage, Toaster, toast | Empty/error/offline/partial state and token-styled notifications |
| Tables | DataTable, DataColumn | Stable IDs, exact values, sorting, global/column filters, visibility/order/pinning/resize, selection, hierarchy, optional paging |
| Trees | ResourceTree, TreeNode | Unique IDs, flattened virtual hierarchy, levels/positions, keyboard selection and expansion |

Schema-driven forms are a separate [package](schema-forms.md). Editors,
visualizations and platform-aware compositions have separate package boundaries.

## Large data and lifecycle

Tables and trees use TanStack Virtual and render only the viewport plus overscan.
The catalog exercises 10,000 rows/nodes. IDs must remain stable across data
updates; array indexes are not identities. Table selection is retained by ID,
including across filters and data refreshes. Consumers decide when removed IDs
should be discarded. Trees accept an acyclic hierarchy with unique IDs.

Table arrows/Home/End navigate rows; Space selects; Enter activates. Column
resize supports keyboard arrows, and column order/pinning have explicit controls.
Tree arrows expand/collapse/move to parent/child, Home/End jump, Space selects,
Enter activates and typing searches visible labels.

Async choices abort obsolete searches and ignore late results. Mutating controls
do not replay failed requests automatically. Upload callbacks must honor the
signal; cancelling suppresses further progress/result changes even if the
transport completes late. A retry starts a separate attempt.

TimeRangeControl lazily loads Grafana behind a public adapter; raw values are
strings and Grafana types stay internal. DateTimeInput itself represents calendar
text: callers must apply a timezone before converting it to an instant. Refresh
intervals are milliseconds; zero disables automatic refresh. Only one refresh
may run at a time, and unmount aborts it.

## Verification

`make web-check` builds packages and fixtures, checks TypeScript, runs unit and
browser acceptance, builds Storybook and checks it in dark/light themes. Catalog
families cover primitives, composed controls, form validation/failure recovery,
large tables/trees, uploads, time controls, unavailable/readonly/empty states and
schema forms. Tests include keyboard/focus interactions and WCAG AA scans.

## Foundations and runtime helpers

`Icon`, `UserAvatar`, `UsersIndicator`, `FeatureBadge`, `FilterPill` and `Segment`
use the same tokens and controls as other families. `ErrorBoundary` isolates a
failed subtree and supports explicit reset or `resetKey`. `useClickOutside`
accepts multiple refs and respects composed event paths; `useDelayedSwitch`
cleans up its timers. The Foundations stories cover utility recovery and token/
typography inspection in both modes.

## Authoring guidance

Use semantic tokens for every surface, border, focus ring and status. Keep
English text in a namespace; package defaults are English and modules install
`module.<service>` resources. Use short action verbs, explicit nouns for labels,
and actionable errors that preserve the user's input. Do not present internal
transport or deployment settings as product tasks.

Label controls, preserve upstream keyboard behavior, and make overlays return
focus. Virtual tables support pointer activation and Enter independently of row
selection; nested buttons/inputs do not also activate the row. Graphs provide
exact-value tables and textual status alongside colors. Automated browser checks
use axe in both themes; this is an acceptance baseline, not a claim that every
possible author composition is automatically accessible.
