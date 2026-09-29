# Implementation scope

The active scope is steps 1–9 below. Application modules such as ErrOtel,
Kratos wrappers and mail services are excluded. Step 9 uses kit fixtures and
representative workflows, not an application migration. Existing hello and
formatter examples remain the platform acceptance fixtures.

Product UI work is being transferred to another agent. The current screen design
is not accepted; see [UI handoff](ui-handoff.md) for code locations, runtime
contracts, pending workflows and the boundary for independent backend work.
The local [engineering baseline](handoff-baseline.md) records the shared API/SDK
contracts and current validation limits, including pending live dev/HMR checks.

| Step | Deliverable | State | Acceptance |
| --- | --- | --- | --- |
| 1 | ObsService and Victoria query driver | implemented | Authenticated queries/discovery and trace lookup against real independent storage; bounds, cancellation, failure and precision checks |
| 2 | Durable audit, outbox and AuditService | implemented | Transactional mutation/audit; durable external intent/results; two-replica outage recovery |
| 3 | Complete API/client/React contracts and method reference | implemented | Generated clients include new services; documented errors/watch recovery; session and subscription failure checks |
| 4 | Full base UI kit and schema forms | implemented | Component matrix coverage, English resources, both themes, keyboard/focus and Storybook states |
| 5 | Editors, charts, observability and platform compositions | implemented | Query languages, large datasets, exact values and lazy loading; generic props separate from API-aware compositions |
| 6 | Complete standalone and embedded module SDK | implemented | External author workflow without platform; same module embedded with compatible shared context and service-owned delivery |
| 7 | Full make dev and live hello/formatter UI | implemented | One command, seeded binding/rule, real configuration/operations/audit/telemetry |
| 8 | Product console M1–M3 | shell implemented; screen composition in progress | Approved header/tree navigation; live shell acceptance, then remaining product workflows |
| 9 | Validate kit against error investigation workflows | implemented | Representative search/detail/stack/log/trace screens in the component catalog, no ErrOtel module implementation |

Twelve public package directories now cover API/client/React, theme/base UI,
schema forms, editors/charts, generic observability, platform compositions and
module SDK/build. The full OTel stack remains deployment-owned and independent.
The OTLP proxy forwards opaque payloads with transport bounds.

Documentation and appropriate tests are updated with each step. Work is performed
without subagents. Stop for unresolved product decisions and for reviewable UI
design before committing to screen implementation. Do not start steps 10–12
(remaining unrelated cleanup, production packaging/CI and publication) under
this scope; checks needed to validate steps 1–9 are included.

Contracts: [frontend/M3](frontend-platform.md), [component matrix](ui-components.md),
[OTLP boundary](telemetry-admission.md).

Step 1 evidence: [ObsService contract](obs-api.md), generated Go/TS clients,
`go test -race ./...`, Go/proto lint, TypeScript typecheck and real
`make test-otlp` with ObsService queries. No application module was implemented.

Step 2 evidence: [AuditService contract](audit-api.md), `make test-audit`,
`make test-replicas` including real AuditEntry delivery, M1/M2 conformance,
full Go race tests, clean Go lint and generated Go/TypeScript clients.

Step 3 evidence: [72-method reference](api-reference.md) and [client contract](api-client.md),
typechecked generated call examples, 27 unit cases, five browser scenarios and
real Go/browser acceptance. Session revoke/logout storage failures now propagate
without clearing the cookie or falsely reporting successful revocation.

Steps 4–5 evidence: [base kit](ui-kit.md), [schema forms](schema-forms.md),
[editors](editors.md), [charts](charts.md), [observability](observability-ui.md)
and [platform compositions](platform-ui.md). `make web-check` passed with 45 unit
cases, seven standalone/embedding/compatibility browser scenarios, 72 Storybook
checks across both themes, and a real Go-console browser test. Schema forms use
`@gopherex/schemapb@1.7.0` and its authoritative CEL/default/validation engine.
Go and proto generation also use the matching release. Full Go race tests and
Go/proto lint pass. The matrix distinguishes supported equivalents and excluded
Grafana-only/deprecated surfaces.

Step 6 evidence: module workbench uses the complete runtime package family;
standalone/container and embedded CSP/deep-link acceptance. `yarn test:packed`
installs twelve tarballs in independent consumers, rejects workspace symlinks,
typechecks/builds, then runs the same browser scenarios. Package dedupe includes
all shared runtime contexts. External installation, per-file package-content
verification, standalone dev/build and embedded browser checks pass. The
standalone Docker image also passed both browser scenarios.
No frontend packages have been published.

Step 7 evidence: `make dev` starts the complete compose installation, seeds the
named binding/rule and returns `Welcome, Developer!` through Envoy. `make test-dev`
passes live service-client relay on the host socket, config validation/save,
hook execution, audit, stored logs/metrics, themes and logout. See
[development](development.md). The live host includes the product console;
the technical component fixture is available separately at `/backplane/dev`.

Step 9 evidence: `Workflows/Error investigation` composes search, stack/source,
logs, trace and return navigation; both dark/light browser flows and axe checks
pass. No application module or error grouping backend was implemented.

Step 8 shell: header, fixed Services/Explore/Audit entries, service-owned page
tree, resizing/pinning, mobile navigation, theme preferences and live module
loading. Services includes catalog/administration; Explore and Audit currently
compose the existing platform controls. See [console layout and remaining
work](console-review.md). This does not mark every product workflow complete.
Shell acceptance checks the live registry and hello routes, service administration,
pointer/keyboard resizing, persisted pinning/theme, mobile focus/navigation and
axe in both themes. Typecheck, seven existing browser scenarios and live dev
API/component acceptance also pass.

Dependency release: [schemapb v1.7.0](https://github.com/gopherex/schemapb/releases/tag/v1.7.0),
commit `259c447`, Go/TypeScript CI and GitHub Packages installation verified.
