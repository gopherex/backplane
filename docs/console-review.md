# Product console layout

Status: navigation structure approved; the current shell is a functional draft,
not an accepted visual design. UI work is handed to another agent; see
[UI handoff](ui-handoff.md). The component fixture at `/backplane/dev` remains
separate from product navigation.

## Shell

Use the existing HyperDX-derived dark/light tokens, compact 14px typography and
one platform-owned header and left navigation tree. The top of the sidebar
contains Services, Explore and Audit. Below it, service branches come from the
live catalog and their page leaves from the plugin navigation registry.
Modules own the labels, relative routes, page count and content of their group.
Services without a UI link to platform administration. Each module branch also
has an explicit administration link. The host owns authentication, theme, route mount and loading/error
states. Deployment settings and storage topology are absent from these pages.

The sidebar uses an icon rail when collapsed, temporary expansion on hover or
keyboard focus, and a pin button. Expanded width is adjustable by dragging or
keyboard; width and pinning persist locally. The visual treatment uses compact
service branches, vertical connectors and a green active-page marker. Stroppy
Cloud is the behavioral reference for rail/expansion/pinning, not a copied
layout. Mobile uses an overlay drawer with focus containment and Escape dismissal.
Module removal removes its navigation and route content; changing a bundle hash
loads and validates the new descriptor. One failed module does not hide others.

The first product landing page is Services. It lists version, instances,
readiness and transport state, with filtering and a link to each service.
Inside a service, use tabs for Overview, Configuration, Operations, Automation,
Events and Runs. The module's own pages remain separately registered at
`/s/<service>/...`; platform service administration lives at
`/services/<service>/...` so it cannot collide with module routes.

Explore is an installation-wide workspace for logs, metrics and traces. Keep a
signal/query/time toolbar above results, with source discovery beside it and
span/log detail in a resizable panel. Preserve query, selected source and time
range in navigation. Expose native languages according to capabilities; do not
invent a universal query language or require module telemetry declarations.

Audit is a filterable installation-wide list with actor/action/subject/outcome,
a detail panel and visible reconnect/cursor-expiry states. Service views can
link to Audit with their filter preselected. Commands require explicit action;
uncertain mutation outcomes are never silently repeated.

## Routes

| Route | Owner / screen |
| --- | --- |
| `/services` | Platform catalog; landing page |
| `/services/:service` | Platform service overview and administration tabs |
| `/explore` | Platform logs/metrics/traces workspace |
| `/audit` | Platform control audit |
| `/s/:service/*` | Service-owned plugin routes |

These paths are relative to the console prefix. Build with
`BACKPLANE_CONSOLE_BASE` matching the backend prefix (default `/backplane/`).

## Remaining product work

The shell, responsive navigation, Services list/detail and routes are implemented.
Configuration, operations, events, runs, schedules, Explore and Audit use the
existing API-aware kit compositions. Further page-specific design remains:

1. Configuration and Automation screens with schema-assisted input, previews,
   revision comparison and explicit command outcomes.
2. Explore and Audit, preserving filters/navigation and supporting large inputs.
3. Live acceptance against hello/formatter and independent telemetry storage,
   in both themes, keyboard navigation and failure/reconnect scenarios.

The kit already supplies the building blocks. Binding/rule controls currently
include contract JSON editing, validation and revision diff; product pages may
add structured step composition after the interaction design is reviewed.
The console does not introduce ErrOtel, another application module, or a
platform-owned error grouping engine.
