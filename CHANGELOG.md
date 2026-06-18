# Changelog

All notable changes to pve-cli. Format based on [Keep a Changelog](https://keepachangelog.com/);
versioning is [SemVer](https://semver.org). While on `0.x`, the CLI/config/`json`
surface may change between minor releases.

## [Unreleased]

## [0.5.7] — live-hardening (found provisioning a real VM via the cluster API)
- **Fixed**: `DELETE` (and other non-body methods) sent params as a form body,
  which PVE rejects (`501 Unexpected content`) — `pc vm delete --purge` failed.
  Params for GET/DELETE now go in the query; only POST/PUT/PATCH carry a body.
- **Added**: `--wait`/`--no-wait`/`--wait-timeout` on `vm/ct clone`, `delete`,
  `migrate`, `create` (previously only on power verbs); shared `waitFlags` helper.
- **Added**: `vm/ct clone --storage` to target a pool on a full clone.

## [0.5.6] — live-verified PDM (found against a real PDM 1.1 endpoint)
- **Fixed**: ticket auth captures the `Set-Cookie` (PDM uses an HttpOnly
  `__Host-PDMAuthCookie` with no body `ticket`); fall back to the body ticket for PVE.
- **Fixed**: PDM provider — remotes live at `/remotes/remote`; resource types are
  prefixed (`pve-qemu`/`pve-lxc`/`pve-node`); render short node hostnames.

## [0.5.5] — offline packaging
- Vendored Go modules in-tree + Debian `.dsc` so OBS builds `.deb`/`.rpm` offline.

## [0.5.4] / [0.5.3] / [0.5.2] / [0.5.1] — packaging, licensing, build
- **Packaging**: OBS project `home:ciriarte:pve-cli` for Debian 12/13,
  Ubuntu 22.04/24.04, Rocky 9/10, openSUSE Leap 15.6/16.0, Slowroll, Tumbleweed
  (x86_64 + aarch64). `tar_scm` + `go_modules` for offline builds.
- **License**: adopted **Apache-2.0** (`LICENSE` + `NOTICE`; SPDX; debian/copyright).
- **Build**: lowered the `go.mod` floor to `go 1.22` (downgraded
  `golang.org/x/time` to a low-floor release) so distro toolchains can build;
  per-distro spec `BuildRequires` (`go` on SUSE, `golang` on RHEL).
- **Docs**: `make docs` generates man pages, shell completions, and a markdown
  command reference (`cmd/gendocs`).

## [0.5.0] — M5: plugins
- **Added**: subprocess plugins — `pc-<name>` on `$PATH` or the plugin dir is
  exposed as `pc <name>` (stdio/args/exit propagated); `pc plugin list`. Built-in
  commands take precedence.
- **Fixed**: `pc config set` coerces scalar types so typed fields
  (`tls.verify`, `rate_limit.qps`) round-trip instead of breaking config load.

## [0.4.0] — M4: Proxmox Datacenter Manager
- **Added**: `pdm` provider over the shared transport; `pc remote list/show`
  (capability-gated, refused on PVE); cross-remote guest/node listing (with a
  `remote` column); global `--provider` flag; provider-aware `pc raw` (walks the
  embedded PDM schema). Proxied per-remote lifecycle returns a clear pointer to
  `pc raw` (deferred).

## [0.3.0] — M3: daily-driver breadth
- **Added**: `pc config`/`context`/`auth login` (keyring secrets); **ticket
  auth** (user+password, cookie+CSRF, auto-refresh); curated guest breadth —
  `create`, `clone`, `migrate`, `config --set`, `snapshot`; `storage list/show`,
  `storage content list`; `backup create/list`; unified `pc guest list`.

## [0.2.1] — fixes (found via live PVE 9.1.6 testing)
- **Fixed**: token-auth task polling — UPIDs containing `!` were double-encoded
  (`%2521`), so `pc task wait`/`log` and `--wait` failed for token auth.
- **Fixed**: `pc api` gained `-d` shorthand; JSON 500 error bodies now surface
  their `message` instead of rendering blank.

## [0.2.0] — M2: generated raw coverage
- **Added**: API-schema ingest (`cmd/schemagen`) + embedded snapshot; schema-driven
  `pc raw` (discovery, describe, execute) for full API reach; golden + breaking-diff
  tests; `pc task log --follow`; explicit `--wait`; client-side rate limiting.

## [0.1.0] — M1: MVP
- **Added**: Go binary `pc`; token auth; TLS fingerprint pinning; direct-PVE
  provider with node auto-resolution; `node`/`vm`/`ct` list·show·power; `task
  show/wait`; `pc api` escape hatch; table/json/yaml output; documented exit codes.

[Unreleased]: https://github.com/ciroiriarte/pve-cli/compare/v0.5.4...HEAD
[0.5.4]: https://github.com/ciroiriarte/pve-cli/releases/tag/v0.5.4
[0.5.3]: https://github.com/ciroiriarte/pve-cli/releases/tag/v0.5.3
[0.5.2]: https://github.com/ciroiriarte/pve-cli/releases/tag/v0.5.2
[0.5.1]: https://github.com/ciroiriarte/pve-cli/releases/tag/v0.5.1
[0.5.0]: https://github.com/ciroiriarte/pve-cli/releases/tag/v0.5.0
[0.4.0]: https://github.com/ciroiriarte/pve-cli/releases/tag/v0.4.0
[0.3.0]: https://github.com/ciroiriarte/pve-cli/releases/tag/v0.3.0
[0.2.1]: https://github.com/ciroiriarte/pve-cli/releases/tag/v0.2.1
[0.2.0]: https://github.com/ciroiriarte/pve-cli/releases/tag/v0.2.0
[0.1.0]: https://github.com/ciroiriarte/pve-cli/releases/tag/v0.1.0
