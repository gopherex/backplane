# Product console layout

The console is a desktop operator workspace. Screens are composed only from kit
and platform compositions ([UI kit](ui-kit.md), [platform compositions](platform-ui.md)),
so the shell, platform pages and module pages share one look. The component
fixture at `/backplane/dev` is a technical acceptance page outside navigation.

## Shell

- Header: brand, breadcrumbs (`Services / hello / Configuration`), a search and
  jump command palette (⌘K / Ctrl+K: services, every service tab, module pages,
  theme), connection state, theme switch and the session menu (current session,
  managing and revoking sessions, log out).
- Navigation: Services, Explore and Audit, then one branch per service from the
  live catalog with a health dot, its module pages as leaves and a manage link.
  Only the current destination is highlighted. The rail collapses to icons,
  expands temporarily on hover or focus, can be pinned and resized (keyboard and
  pointer); width and pinning persist per browser.
- Pages fill the window. Headers and filter bars stay in place; panels scroll
  their own content with sticky table headers. Banners report a lost connection,
  a stale catalog and failures to load modules.

## Screens

| Route | Screen |
| --- | --- |
| `/services` | Stat tiles (services, healthy instances, attention, bound hooks, rules, workflows) and the catalog as cards, a table or the system map |
| `/services/:service` | Entity header (health, version, instances, SDK, uptime) with tabs |
| `…/` (Overview) | Instances and instance drawer, contract panels, component tree, metadata |
| `…/configuration` | Live settings editor, rollout, revisions with diff and rollback |
| `…/automation` | Bindings of the service's hooks and rules on its events |
| `…/operations` | Call hooks, run activities, publish test events, start workflows |
| `…/events` | Streams, published/consumed events, subscribers, messages, dead letters |
| `…/workflows` | Declared workflows, runs, schedules |
| `…/telemetry` | Explore scoped to the service's telemetry selectors |
| `…/audit` | Audit entries attributed to the service |
| `/explore` | Logs, metrics and traces workspace; query state in the URL |
| `/audit` | Installation audit with value pickers, live tail and entry drawer |
| `/s/:service/*` | Module-owned pages |

Paths are relative to the console prefix (`BACKPLANE_CONSOLE_BASE`, default
`/backplane/`). `/services/<service>/…` belongs to the platform and
`/s/<service>/…` to the module, so they cannot collide. Modules own the labels,
routes, page count and content of their branch; removing a module removes its
navigation, and one failed module does not hide the others.

## Principles

Color carries status only. Destructive and installation-changing actions ask
for confirmation and never repeat automatically after an uncertain outcome.
Where a filter has known values, it offers them. The console introduces no
error-grouping engine, deployment settings or telemetry storage management.
