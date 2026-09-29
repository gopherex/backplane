# Engineering baseline for UI handoff

## Ownership and shared contracts

The UI agent owns visual design and product workflow composition in
`web/apps/embedding`, component styles/stories and corresponding UI tests.
The existing visual design is not accepted. The brief is [UI handoff](ui-handoff.md).
The backend lane owns `internal/`, `pkg/backplane/`, migrations and Go conformance.

Preserve these shared contracts during UI work. A needed change should identify
the affected screen, API method and consumer first; update source contracts,
generated clients, documentation and consumer checks together.

| Boundary | Baseline | Source of truth |
| --- | --- | --- |
| Console API | `backplane.console.v1`, 72 methods, protobuf 64-bit values as bigint | `backplanepb/console/v1/*.proto`, `web/packages/api/src/gen`, [API reference](api-reference.md) |
| Sessions | Operator-token exchange, HttpOnly cookie, relative login/session/logout endpoints | [client contract](api-client.md), `internal/console/http.go` |
| Transport | One host-owned ws-proto connection, cancellation, distinct snapshot/cursor semantics, no mutation replay | `web/packages/client`, `web/packages/react` |
| Module SDK | Major 0, relative routes/nav, host providers, catalog-validated hash/SDK, service-owned delivery | `web/packages/plugin-sdk`, `web/packages/plugin-build`, [frontend contracts](frontend-platform.md) |
| Routes | Platform `/services/:service/...`; module `/s/:service/*`; Services/Explore/Audit before module branches | [console structure](console-review.md) |
| Packages | Twelve `@gopherex/backplane-*` workspaces at 0.1.0; GitHub Packages target; pinned Yarn lockfile | `web/packages/*/package.json`, `web/yarn.lock` |
| Forms | schemapb 1.7.0 validation/defaults/masking; preserve large integer strings | [schema forms](schema-forms.md) |
| Audit | Database mutations/audit in one transaction; external command intent/results; cursor expiry explicit | [audit contract](audit-api.md) |
| Telemetry | Deployment-owned OTel; ObsService reads; capability-driven languages; optional declarations | [ObsService](obs-api.md), [admission](telemetry-admission.md) |

Coordinate edits to `Makefile`, compose, protobufs/generated clients, package
manifests/lockfile and plugin build/runtime contracts. Presentational APIs may
evolve with their consumers; breaking public runtime contracts needs an explicit
compatibility decision. Visual changes must not fork the transport or SDK.

## Checkpoint scope

The baseline includes accumulated audit/observability/backend integration,
frontend packages, author template, live examples, technical acceptance fixtures,
draft shell/login, documentation and the console development proxy. It is a
development starting point, not a production release or visual acceptance.
CI, production packaging/publication and application modules remain deferred.

The local branch `handoff/ui-foundation` contains this checkpoint. It is not
pushed or tagged. Create UI worktrees from that branch rather than the older
`d3bb94a` base.

Review covers transaction/audit wiring, session errors, telemetry boundaries,
generated contracts, package/runtime ownership, acceptance tests and accidental
artifacts/credentials in candidate source files. It does not certify every
product workflow or load scenario.

## Validation status

- Go race suite passed using existing test cache. Environment-gated live
  conformance was not rerun as part of that command.
- Go/proto lint passed with zero issues.
- Full frontend package/application builds and TypeScript passed.
- 49 unit cases passed, including dev-proxy endpoint selection, origin checks,
  prefix handling and cookie preservation.
- Vite dependency optimization passed after isolating Grafana's private router
  versions from the host's shared router in the optimizer pipeline.
- Generation completed for Go, TypeScript, hello, store queries and the
  72-method API reference; all 66 captured generated/contract files matched their
  pre-generation content (no drift).
- The full browser/Storybook run passed seven browser cases and 70/72 Storybook
  cases. Two failures came from a binding test expecting an invalid empty-step
  draft to validate successfully. The corrected test asserts rejection first,
  then validates/saves a nonempty definition. Browser rerun is pending.
- Live proxy/HMR acceptance is implemented in `web/tests/console-dev.mjs`, but
  pending: the current execution sandbox rejects localhost listening with
  `EPERM` and cannot reach the already-running installation.

Do not describe pending browser checks as passed. Restore local network access
and run the checks below before calling the baseline fully verified.

## Remaining verification

At repository root, `rtk proxy make dev` prepares the installation. From `web/`
in another terminal, start `rtk proxy yarn dev:console`. Then from `web/`:

```sh
rtk proxy yarn test:console-dev
rtk proxy env BACKPLANE_DEV_URL=http://127.0.0.1:5173 node tests/login-browser.mjs
rtk proxy env BACKPLANE_DEV_URL=http://127.0.0.1:5173 node tests/console-browser.mjs
rtk proxy env BACKPLANE_DEV_URL=http://127.0.0.1:5173 node tests/dev-browser.mjs
rtk proxy yarn test:packed
```

At repository root, run `rtk proxy make web-check` for the corrected Storybook
scenario and real Go console/browser integration. Do not run host conformance
sharing xDS/example identities alongside the same dev installation.

The HMR check verifies real cookie login, module assets/RPC, deep links,
CSS/React hot updates without draft/session loss, foreign-origin rejection and
logout. It temporarily changes two shell files and restores them; run it without
concurrent editing. Cleanup refuses to overwrite concurrent changes.
