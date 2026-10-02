# Releases and containers

`make release` checks a clean working tree and an upstream-synchronized branch,
then creates and pushes a `vX.Y.Z` tag. It does not build, install dependencies or
run tests locally. The release workflow requires successful push CI for that
commit, then builds the deployable artifacts. It does not rerun the full stack
verification suite.

## Version contract

The server tag and all 13 public `@gopherex/backplane-*` packages must agree on
the version. Before tagging, update the package manifests and their internal
dependency versions together and commit the result. The initial frontend line
is **0.1.0**, so the first coordinated tag is **v0.1.0**. Version validation
fails before building or publishing if these values diverge.

Frontend plugin compatibility is currently SDK major **0**. A 1.x release
requires updating and validating the host/module compatibility contract; a
version bump alone does not establish compatibility.

## What a release publishes

| Destination | Artifact |
| --- | --- |
| GitHub Releases | Linux amd64/arm64 archives: `bin/backplane`, `console`, license and launch instructions |
| GitHub Releases | 13 frontend `.tgz` packages and `frontend-packages.json` inventory |
| GitHub Releases | `SHA256SUMS` and `image-digest.txt` |
| GHCR | `ghcr.io/gopherex/backplane:X.Y.Z` and `:latest`, both Linux amd64/arm64 |
| GitHub Packages | All 13 `@gopherex/backplane-*` packages at the coordinated version |

The multi-platform image is assembled from the same binary and console trees
as the downloadable archives. It runs as non-root and includes trusted CA
certificates. Its defaults set `BACKPLANE_CONSOLE_ASSETS_DIR=/app/console` and
`BACKPLANE_CONSOLE_PREFIX=/backplane`. Deployment supplies credentials and
infrastructure endpoints; see [configuration](../deployments/README.md).

GitHub Release is created after image and package publication succeeds. A
failed publication can leave some registry artifacts published; rerun that tag
after correcting access. A retry reuses an existing frontend version only if
its integrity matches the archive. Different contents require a new version;
the workflow never deletes published packages to overwrite them.

## Rehearsal

Run **Release → Run workflow** on the intended branch with version `0.1.0`.
It builds archives, packages and the multi-platform container without pushing
images, publishing packages or creating a release. Download `release-files`
from the workflow's artifacts. A rehearsal proves the builds; registry write
permissions are exercised by an actual tagged release.

Local equivalents, after installing dependencies:

```sh
bash scripts/build-release.sh 0.1.0
docker buildx build --platform linux/amd64,linux/arm64 \
  -f deployments/Dockerfile.release bin/release
```

The standalone source Dockerfile builds the frontend and server itself:

```sh
docker build --secret id=github_token,env=NODE_AUTH_TOKEN \
  -f deployments/Dockerfile -t backplane:local .
```

Use a package-read token for `NODE_AUTH_TOKEN`. BuildKit mounts it only during
installation; it is not a Docker build argument or included in the runtime
image. Frontend authentication follows the
[GitHub Packages setup](../web/README.md).

## Workflow access

The standard `GITHUB_TOKEN` needs package-read access to existing Gopherex
dependencies, package-write access for GHCR/frontend publication and
contents-write access for release creation. Grant this repository Actions
access on dependency package settings if needed. Local builds require a token
with package-read access. Published package visibility and organization policy
are controlled in GitHub's package settings.

No publication goes to npmjs.org. The release workflow explicitly uses
`https://npm.pkg.github.com` and rejects a public package manifest configured
with another registry.
