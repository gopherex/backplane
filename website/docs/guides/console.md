# Use the console

The console has a header, a collapsible/resizable left navigation and a routed content area. Platform pages come first; each service can contribute a tree of module pages beneath them.

## Login and navigation

The installation's operator token is exchanged for an HttpOnly session cookie. It is not an OTLP ingest key or a service internal secret. The login screen retains a requested deep link after authentication. Session expiry returns you to authentication; a temporary backend failure must not masquerade as invalid credentials.

The sidebar can collapse to a rail, expand on hover/focus and be pinned. Drag its separator to resize, or focus it and use arrow/Home/End keys. Double-click resets width. A narrow viewport uses a navigation drawer. Theme and layout preferences stay local to the browser.

## Platform pages

| Page | Purpose |
| --- | --- |
| Services | Catalog, service status and access to administration |
| Wiring | Connect hooks and events, edit definitions and inspect runs |
| Explore | Query stored logs, metrics and traces across discovered sources |
| Audit | Durable platform and trusted application history |
| Errors | OTel exceptions, browser state/history and related telemetry |
| Infrastructure | Configured dependency status and available capabilities |

Service administration includes configuration, API reference, wiring, events, workflows/schedules, telemetry, audit and errors where applicable. Capability state distinguishes a disabled feature from an empty result.

Module pages are separate routes below `/s/:service/*`. Their labels and count come from the module. Opening service administration does not require that service to ship a custom UI.

## Read status carefully

- A healthy instance is not proof that every downstream workflow can complete.
- A saved configuration revision may still be pending or rejected by an instance.
- A successful workflow start is not a successful workflow result.
- A telemetry query with no rows is different from an unavailable store.
- Infrastructure OK is a bounded connectivity check, not end-to-end telemetry validation.

## Working safely with forms

Actions preserve drafts after failure and suppress duplicate in-flight submissions. An uncertain mutation result requires inspection before retry. Revision conflicts show the latest state so you can reconcile changes; they must not be dismissed by repeatedly forcing the same draft.

Use [configuration](configuration.md), [wiring](wiring.md), [workflow](workflows.md) and [event](events.md) guides for specific tasks. Authors can reuse these views through [platform-ui](../reference/platform-ui.md).
