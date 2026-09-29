# UI implementation handoff

## Assignment and acceptance

Own the product UI of Backplane and complete the operator workflows using the
existing API, client, module SDK and component packages. The current screens are
a functional starting point, **not an accepted visual design**. The user is
transferring UI work to another agent because the current visual execution is
unsatisfactory. Do not treat passing browser/axe checks as design acceptance.

The accepted information architecture is a header and a left navigation tree:
Services (landing), Explore and Audit at the top; service branches below, with
their module-owned pages as leaves. Modules determine their own page names,
count and content. Preserve this structure while redesigning the presentation.

Use HyperDX-derived colors/tokens and compact operator-console density, with
both dark and light themes. The stack is React, the shared shadcn-based kit and
Grafana adapters. English i18n only. Stroppy Cloud's sidebar is a behavioral
reference for collapse, temporary expansion and pinning; do not copy its design
verbatim. The local reference is
`/home/yaroher/devel/github/stroppy-io/stroppy-cloud/web/src/app/Sidebar.tsx`.

## Working copy

Work in `/home/yaroher/devel/github/gopherex/backplane`, not in the umbrella
directory as a Go module. The local handoff branch is `handoff/ui-foundation`;
its checkpoint includes backend, docs and frontend work accumulated after
`d3bb94a`. Use the checkpoint, not the old base, when creating the UI worktree:

```sh
rtk proxy git worktree add -b ui/console ../backplane-ui handoff/ui-foundation
```

Install/build dependencies in the new worktree; ignored `node_modules`, `dist`
and Go binaries are not part of the checkpoint. Preserve unrelated working-tree
changes; do not reset or clean the shared checkout. This is a local checkpoint,
not a pushed branch or release. Its [validation limits](handoff-baseline.md)
must be retained in the handoff.

Read applicable user/repository instructions. Do not spawn agents, publish,
commit or push unless authorized. Shell commands use `rtk`; prefer the codebase
knowledge graph for code discovery. Go commands use `GOWORK=off` in this module.

## Available foundation

| Area | Existing implementation | Qualification |
| --- | --- | --- |
| Backend | Console RPC/relay, configuration, bindings/rules, operations, events, workflows/schedules, durable audit, ObsService/Victoria, OTLP forwarding | Product workflows still need UI composition and verification |
| API transport | Generated protobuf/ws clients, cookie-session client, React providers/hooks | One host connection shared with module clients; no automatic mutation replay |
| Kit | Twelve `@gopherex/backplane-*` packages, primitives, forms, editors, charts, observability and platform compositions | Availability is not proof of polished UX or full Grafana behavioral parity |
| Module SDK | Standalone author template, embedded routes/navigation, shared runtime and bundle validation | Authors can run their module without Backplane; service serves its deployed bundle |
| Dev stack | Backplane, hello, formatter, seeded hook binding and event rule, independent telemetry stack | Local example fixtures, not production packaging |
| Console | Login, header/sidebar, Services list/detail, module pages, Explore/Audit routes | Functional draft; visual design and full product workflow completion are pending |

Public packages live in `web/packages/`: `api`, `client`, `react`, `theme`, `ui`,
`schema-forms`, `editors`, `charts`, `observability-ui`, `platform-ui`,
`plugin-sdk`, `plugin-build`. Schema forms use `@gopherex/schemapb@1.7.0`.
Frontend packages are not yet published. Gopherex packages use GitHub Packages;
do not change publication to public npm. Use Yarn and the pinned lockfile.

## Start here in code

| Path | Responsibility |
| --- | --- |
| `web/apps/embedding/src/main.tsx` | Runtime/session/theme/i18n bootstrap; live console vs compatibility fixture |
| `web/apps/embedding/src/console/Console.tsx` | Product routes, header, Services and service administration composition |
| `web/apps/embedding/src/console/Sidebar.tsx` | Service/page navigation, rail/pinning/resizing/mobile behavior |
| `web/apps/embedding/src/console/registry.ts` | Catalog watch, module descriptors/loading and isolation |
| `web/apps/embedding/src/console/Login.tsx` | Operator-token login and session-check states |
| `web/apps/embedding/src/console/*.css` | Current screen styles, replaceable as part of design |
| `web/apps/embedding/src/console/*locales.ts` | English screen resources |
| `web/packages/platform-ui/src/` | Existing API-aware compositions to reuse/refine |
| `web/packages/theme/src/tokens.ts` | Shared palette/semantic tokens |
| `web/packages/ui/src/` | Shared primitives/compositions |
| `web/apps/catalog/` | Component examples and Storybook |
| `web/templates/module/` | Standalone and embedded hello UI example |

There is currently no separate `web/apps/console` application. The `embedding`
app's `live` build serves the product console; the default build remains an
isolated SDK compatibility fixture. If separating them, update Makefile, compose
asset mounts, packed-consumer checks and browser fixtures together. The current
bootstrap also imports the template's global `standalone.css`; account for that
coupling when restructuring shell styles.

## Run and inspect

Run at repository root:

```sh
rtk proxy make dev
```

Open `http://127.0.0.1:10000/backplane/`. Default local operator token:
`dev-admin-token-change-me` (`DEV_ADMIN_TOKEN` can override it). This is the
current authentication contract, not an email/password or IAM login. Do not
invent account/password/reset flows without agreeing their backend contract.
The token must not be baked into the app, prefilled or saved in browser storage.

With the dev installation already running, rebuild only the live shell from
`web/`:

```sh
rtk proxy yarn workspace @backplane/embedding build --mode live --outDir dist-live
```

For hot shell iteration, run `rtk proxy yarn dev:console` from `web/` and open
`http://127.0.0.1:5173/backplane/`. It connects to the existing backend through
the local proxy. See [development](development.md) and the exact
[validation status](handoff-baseline.md); live HMR acceptance is still pending
under the restricted execution sandbox.

The dev container mounts `dist-live`; refresh the browser after rebuilding.
If changing a shared package, rebuild that package first. Rebuild all packages
with `rtk proxy yarn build:packages` from `web/`. `yarn dev` starts the component
catalog, not the live product console. Use `yarn dev:console` for the latter.

From `web/`, `rtk proxy yarn storybook` starts the kit catalog and
`rtk proxy yarn dev:module` starts the standalone module template. Registry read
credentials belong in the user's package-manager configuration, never in source
or container mounts.

`/backplane/dev` is the technical component acceptance page, intentionally absent
from product navigation. The template Settings page saves only local state;
it is not a backend settings implementation.

## Preserve runtime boundaries

- `/services/:service/...` belongs to platform administration;
  `/s/:service/*` belongs to the service module. Paths are relative to the
  configured console prefix. Do not hardcode hello into the shell registry.
- The host owns router, theme, i18n and transport lifetime. Remote modules use
  shared providers and the host socket. Keep descriptor/hash/SDK/CSP checks.
- Keep session data isolated across logout or replacement. Transient network,
  429 and 5xx failures do not mean credentials were revoked. Drafts should survive
  a reconnect; uncertain mutations are not silently retried.
- Preserve bigint/nanosecond precision and server-side validation. A saved
  configuration revision is not necessarily applied: expose applied/rejected
  per-instance state.
- Telemetry infrastructure is deployment-owned. UI queries stored logs, metrics
  and traces through ObsService; advertise languages according to capabilities.
  Optional module telemetry declarations are not prerequisites for discovery.
- Do not add ErrOtel/error grouping, Kratos wrappers, other application modules,
  deployment settings or telemetry storage management to this assignment.

## UI work still required

1. Redesign the login and shell coherently, then service list/detail navigation.
   Review the visual direction with the user before propagating it to every page.
2. Complete configuration UX: schema-assisted edits, validation, revision
   comparison/rollback, per-instance applied/rejected status and unsaved drafts.
3. Compose Automation: bindings and rules, operation inputs/results, workflow
   runs, schedules, events and dead letters. Existing editor components expose
   contract JSON; that alone is not a finished operator workflow. Current service
   tabs omit an Automation screen.
4. Turn Explore into a coherent query/results/detail workspace; persist query,
   source and time range in navigation, connect logs and trace/span details.
5. Finish Audit filters, entry details and service-scoped navigation, preserving
   reconnect/cursor-expiry semantics.
6. Review loading, empty, error, permission/session failure, partial results,
   large datasets, keyboard/focus and narrow-screen states throughout.

Use [component coverage](ui-components.md) as an inventory, not as a claim that
all required interactions are finished. See [platform compositions](platform-ui.md),
[schema forms](schema-forms.md), [observability](observability-ui.md),
[API reference](api-reference.md) and [runtime contracts](frontend-platform.md).

## Verification

Recent completed checks include typecheck, seven standalone/embedding browser
scenarios, live API/component acceptance, live shell navigation/axe in both
themes, and login rejection/pending/deep-link/logout/outage recovery. Earlier
foundation checks are recorded in [implementation scope](implementation-plan.md).
Those are previous results, not a fresh full-suite run at handoff.

- Root: `rtk proxy make test-dev` against the running installation; covers
  `login-browser.mjs`, `dev-browser.mjs` and `console-browser.mjs`.
- Root: `rtk proxy make web-check` for packages, types, unit/browser/Storybook and
  the real Go console integration check.
- From `web/`: `rtk proxy yarn test:packed` after changing public package/build
  contracts; verifies installation in external standalone/embedded consumers.
- Inspect screenshots on a laptop and mobile in both themes. Passing automation
  does not constitute visual approval.

Do not run conformance processes owning xDS/example service identities alongside
the same live compose installation. See [development](development.md) for
switching topologies without deleting data.

## Independent backend/engineering lane

UI ownership: `web/apps/embedding`, visual packages/components/stories and UI
browser tests. Coordinate changes to shared package APIs, protobufs/generated
clients, Makefile and compose rather than changing them independently.

Shared API/SDK baselines and file ownership are recorded in
[engineering baseline](handoff-baseline.md). Useful later work, to be scoped
before implementation:

- Complete the pending live dev-proxy/HMR and browser checks once localhost
  access is available; the contracts and local checkpoint are already prepared.
- Prepare CI for code generation drift, Go checks, package builds, browser,
  packed-consumer and integration tests. No repository `.github` workflows or
  platform Dockerfile currently exist; the module template has its own Dockerfile.
- Prepare production packaging and an upgrade/migration smoke path, without
  publishing a release until its scope is agreed.
- Review remaining backend gaps and test coverage against actual contracts.
  Nexus endpoint retirement and replica conformance already have implementations
  and tests; do not repeat the old backlog as if they were absent. Historical
  manifest cleanup needs a separate current-code and retention-policy review.

Production CI/packaging/publication were outside the prior steps 1–9 scope.
This list proposes follow-up work; it does not mark it started or approved.
