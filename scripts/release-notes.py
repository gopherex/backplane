"""Generate deployment links for a GitHub Release, without shell interpolation."""
import pathlib
import sys

version, digest_file = sys.argv[1:]
digest = pathlib.Path(digest_file).read_text().strip()
print(f"""Backplane {version} — server, console and coordinated frontend SDK.

## Container

```sh
docker pull ghcr.io/gopherex/backplane:{version}
```

Linux amd64 and arm64. Immutable reference: `{digest}`.
The image includes the production console under `/backplane/` and runs as a non-root user.

## Downloads

- `backplane_{version}_linux_amd64.tar.gz` / `backplane_{version}_linux_arm64.tar.gz`: server, console, license and launch instructions.
- `gopherex-backplane-*.tgz`: the 13 coordinated frontend packages, also published to GitHub Packages.
- `frontend-packages.json`: package inventory and integrity values.
- `SHA256SUMS`: checksums for all archives and the inventory.
- `image-digest.txt`: the immutable container reference.

[Deployment and configuration](https://github.com/gopherex/backplane/blob/v{version}/deployments/README.md) · [Frontend SDK](https://github.com/gopherex/backplane/blob/v{version}/web/README.md) · [API reference](https://github.com/gopherex/backplane/blob/v{version}/docs/api-reference.md)
""")
