# abouei-checker (npm distribution)

This directory is the **distribution and launcher layer** for the
`abouei-checker` npm package. The Go application in this repository owns all
behavior; these files only provision the platform binary and dispatch to it.

## Strategy

Single npm package (`abouei-checker`) + version-pinned GitHub Release
artifacts, downloaded at install time and SHA-256 verified.

Trade-offs considered:

1. **Platform-specific optional dependencies** — atomic with npm, but N
   packages to own, version, and publish for every release. Rejected: too
   much release machinery for a single-maintainer project.
2. **Download verified binaries from GitHub Releases (chosen)** — one
   package to publish, deterministic pinned versions (never `latest`),
   HTTPS-only, checksum-verified, clear install diagnostics. Cost: install
   requires network access to `github.com`, and `postinstall` must be
   allowed to run. This matches the project's scale and keeps the supply
   chain auditable (one artifact URL + one checksum per platform).
3. **Bundling binaries inside the npm tarball** — rejected: multi-megabyte
   tarballs per platform, registry bloat, no checksum separation.

## Layout

| Path                   | Responsibility                                    |
| ---------------------- | ------------------------------------------------- |
| `package.json`         | Package metadata, `bin` mapping, lifecycle scripts |
| `bin/abouei-checker.js`| Executable dispatcher (args, stdio, exit codes)   |
| `lib/platform.js`      | OS/arch → Go target mapping and asset naming       |
| `lib/install.js`       | `postinstall` download, verify, extract            |
| `lib/check-versions.js`| Fails on `package.json` ↔ `VERSION` drift         |
| `test/`                | `node --test` suites (no dev dependencies)         |

## Versioning

One version everywhere: the repository `VERSION` file, `npm/package.json`,
the Go linker flag (`-X .../buildinfo.Version=`), the git tag (`vX.Y.Z`),
and the GitHub Release artifacts. `npm run check-versions` enforces the
npm ↔ `VERSION` link; the release workflow enforces the rest.

## Install behavior

- Supported: `linux-x64`, `linux-arm64`, `darwin-x64`, `darwin-arm64`,
  `win32-x64`. Everything else fails with a diagnostic pointing at
  source/Docker installs.
- `ABOUEI_CHECKER_SKIP_POSTINSTALL=1` skips provisioning (packaging/CI).
- `ABOUEI_CHECKER_BINARY=/path/to/binary` overrides dispatch (tests,
  advanced setups). The provisioned versioned binary remains the default.
- Corrupt downloads fail closed: checksum mismatch or unexpected archive
  contents abort the install and never execute the binary.

## Security notes

- HTTPS only; redirects followed within `https.get`.
- SHA-256 of the archive is verified against `<asset>.sha256` before
  extraction; extraction expects exactly one file.
- No privileged commands, no shell-profile edits, no services.
- Tokens must never appear in URLs; `redactUrl` guards error output.

## Until the first release

Installable binaries appear once GitHub Release `v0.6.0+` artifacts exist.
Until then, `npm install` from this directory provisions nothing and the
launcher reports the missing binary with recovery steps. Local testing:

```bash
cd npm
npm test                    # platform mapping + launcher plumbing
npm run check-versions      # version alignment
npm pack --dry-run          # package contents
```
