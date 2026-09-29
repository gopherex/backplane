# Module UI template

In this repository run `yarn dev:module` from `web/`. In a generated independent
module, install the published packages and run `yarn dev` here. A standalone
development server uses explicit local fixtures; it does not require a platform.

- `plugin.config.json`: service and navigation entries (relative paths and i18n keys).
- `src/definition.tsx`: any number of module-owned pages under the host route.
- `src/locales/en.ts`: English strings in the `module.hello` namespace.
- `src/standalone.tsx`: local router, theme, translations and fixture client.
- `src/embedded.tsx`: page content consuming the platform's providers.
- `src/workbench.tsx`: lazy editor, schema form, chart and correlated logs using
  the same public packages in both entrypoints.
- `src/gen`: hello's generated internal API, called with `useClient` through the
  host socket. `make gen` refreshes it from hello's protobuf definitions.

`yarn build` produces `dist/standalone` and `dist/plugin`. Serve standalone with
SPA fallback for deep links. The service serves `dist/plugin` through its
platform port in a deployment; backplane proxies and loads it from the console
origin. Production integration must verify `sdk_major`, shared dependencies,
relative asset paths and the console CSP before publication. The repository's
embedding fixture verifies these in a browser under `/backplane/s/hello/`.

## Standalone container

After `yarn build` in `web/`, run from the repository root:

```sh
docker build -t backplane-module-local web/templates/module
docker run --rm -p 127.0.0.1:8080:8080 backplane-module-local
```

Open `http://127.0.0.1:8080/` or `/settings`. The image contains only the built
standalone site and a static server with SPA fallback; no platform processes,
package registry access or credentials are needed at runtime. Its process runs
as the unprivileged `node` user. A module's own CI can build and distribute this
image. For live code editing use `yarn dev`; the container serves a fixed build.

The example settings form deliberately saves only component state; it does not
pretend to persist configuration. Replace fixture adapters and page logic with
your own service clients. Do not store operator credentials in plugin code.

## External-package acceptance

`yarn test:packed` from the workspace root packs all twelve public packages and
installs them into fresh temporary module/host directories. It checks types and
builds without source aliases or workspace symlinks, then runs browser checks
for standalone dev/build/deep links and embedded shared providers/lazy assets/CSP. No
registry publication is performed.

To test an already running standalone container or author dev server:

```sh
MODULE_TEST_URL=http://127.0.0.1:8080 yarn playwright test -c playwright.standalone.config.ts
```

Module routes and navigation remain relative to the host service base. Call
both generated platform and service clients with `useClient`; the host owns the
connection. Abort effects/subscriptions on unmount. A module never creates or
disposes the host router/client. The build contract shares all public runtime
packages with strict compatible versions; code editors and plots load lazily.
The SDK's route contract is cooperative code structure, not a sandbox for
untrusted JavaScript. Modules are trusted code in the same browser origin.
