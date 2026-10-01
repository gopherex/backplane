# Local development

## Module author

From `web`, run `yarn dev:module`. The template owns its router and supplies an
explicit fixture client. It starts without any platform services. The same
routes/components build into a remote with `yarn workspace @backplane/module-template build`.
See [template instructions](../web/templates/module/README.md).

## Platform installation

For UI-only iteration against a running installation, use
`rtk proxy yarn dev:console` from `web/`. It builds shared packages once and
starts Vite at `http://127.0.0.1:5173/backplane/`. Shell TSX/CSS changes update
without rebuilding Go binaries or restarting containers. Shared package changes
still require their package build and a browser refresh.

Vite proxies only `auth/*`, `ws` and `plugins/*` to the existing installation at
`http://127.0.0.1:10000`; application routes/assets and HMR stay on Vite. Cookies
remain HttpOnly; module clients use the host backend socket. HMR has its own
socket, not a second RPC transport. The proxy verifies the browser origin before
rewriting it for backend origin checks. It binds to loopback and does not change
backend authentication or production CSP.

Set `BACKPLANE_DEV_TARGET` to another installation origin if needed, without
credentials, a path or query. `BACKPLANE_CONSOLE_BASE` defaults to `/backplane/`
and must match the backend prefix. After the initial package build,
`rtk proxy yarn workspace @backplane/embedding dev` starts Vite directly.
This command requires a running backend; it does not start Docker or seed data.
See [handoff validation status](handoff-baseline.md) for completed checks and scope.

Run `make dev` at the repository root. Requirements: Docker Compose, Go 1.26,
Node >=22.12, Yarn 1.22.22 and GitHub Packages read credentials in the user's
package-manager configuration. `make configure` installs pinned generators.

The command builds packages, the standalone/remote module and the live platform
console; builds static Go binaries; starts the base stack plus independent OTel
Collector/Victoria stores and backplane/hello/formatter containers; waits for
operator endpoint and all three Consul health checks; installs `hello.Greet -> formatter.Format` and the
`hello.Greeted -> formatter.Record` rule; and sends a sample greeting.

Open `http://127.0.0.1:10000/backplane/`. The local default operator token is
`dev-admin-token-change-me`; override it with `DEV_ADMIN_TOKEN`. The development
console uses an operator token to create a cookie session. Its centered login
screen explains the credential, supports dark/light themes and token visibility,
and distinguishes rejected credentials from rate limits or connection failures.
It prevents duplicate submissions and keeps the requested deep link after login.
The token is not prefilled or saved in browser storage.
The development
host loads the hello plugin from hello's platform port through backplane's
protected, hashed bundle proxy. Its internal `GetStats` client and the platform
clients share one WebSocket. The landing page is Services; Explore and Audit are
the next fixed entries in the left navigation. Service groups contain pages
declared by their modules. Service administration is under `/services/:service`.
The technical kit acceptance fixture remains directly accessible at
`/backplane/dev`; it is not part of the product navigation.

The sidebar collapses to an icon rail, expands on hover/keyboard focus and can
be pinned. Drag its edge or use Left/Right/Home/End on the focused separator to
resize; double-click restores the default. Width, pinning and theme are local
preferences. On narrow screens, the header button opens the navigation drawer.
The console asset prefix is configured at build time with
`BACKPLANE_CONSOLE_BASE` (default `/backplane/`), matching the backend prefix.

`make dev-logs` follows service logs. `make test-dev` runs live API/component and
console navigation acceptance, including both themes, keyboard/mobile behavior
and axe accessibility checks on the shell.
To run it through Vite, use
`rtk proxy env BACKPLANE_DEV_URL=http://127.0.0.1:5173 BACKPLANE_DEV_TARGET=http://127.0.0.1:10000 make test-dev`.
The second origin is used for direct service requests such as `/hello/`, which
are outside the console proxy. From `web/`, `yarn test:console-dev` checks HMR;
it temporarily edits and restores shell TSX/CSS, so do not edit those files
concurrently.
`make dev-down` stops the installation and keeps database/telemetry volumes.
Running `make dev` again rebuilds the services and reapplies the named example
binding/rule; it does not delete stored data. Configuration edits made in the
fixture persist. Server validation checks the schema; service-specific live
validators may still reject a revision. Check per-instance applied/rejected
status before treating it as deployed. The fixture's settings page explicitly saves only local state.

The compose overlays use the same base project as `make up`. Do not run
conformance processes that own xDS or register the same services alongside the
live installation. After `make dev-down`, `make up` restores the host-process
conformance topology. The `docker-compose.dev.yaml` overlay points Envoy at the
container backplane; the base topology points it at the host.

The container runtime uses built binaries and read-only asset mounts. It does
not install frontend dependencies or receive package-registry credentials.
Services advertise their resolved container IPs. The OTel exporters send to the
independent Collector; backplane reads Victoria through ObsService and offers
opaque OTLP admission at `/backplane/otlp/v1/{logs,metrics,traces}`. No telemetry
backend deployment controls appear in the UI. Trace search languages depend on
the backend capabilities; TraceQL is not advertised unless enabled by deployment.

## Acceptance commands

- `make web-check`: packages/fixtures, types, unit cases, both browser suites and
  a real Go-console cookie/relay/bundle test.
- `cd web && yarn test:packed`: all public packages installed as tarballs into
  fresh external consumers; standalone and embedded browser checks.
- `make test-dev`: real running installation; does not start it implicitly.
- `make lint` and `GOWORK=off go test -race ./...`: backend checks.

The contract/reference and package guides are linked from
[implementation scope](implementation-plan.md). Application modules (Kratos
wrappers, mail services) are outside this work.

For the console without optional infrastructure, use `make dev-minimal`.
PostgreSQL, Consul and Valkey remain required. The **Infrastructure** page shows
configured dependencies as OK/NO and separately lists deployment capabilities.
See [deployment modes](../deployments/README.md#server-deployment-modes) for
Compose profiles and endpoints. Service **Workflows** includes administrative
schedule creation, editing and deletion; module SDKs never reconcile schedules.
