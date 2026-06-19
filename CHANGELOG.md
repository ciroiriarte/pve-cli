# Changelog

All notable changes to pve-cli. Format based on [Keep a Changelog](https://keepachangelog.com/);
versioning is [SemVer](https://semver.org). While on `0.x`, the CLI/config/`json`
surface may change between minor releases.

## [Unreleased]

## [0.8.0] — PDM control-plane curation by functional domain
- **Added**: curated command groups for PDM's control plane (all PDM-provider
  gated; refused with a clear message on PVE):
  - `pc ceph` — clusters/status/summary/pools/osd-tree/mon/mgr/mds/fs/flags (read)
  - `pc access` — users, tokens, roles, acl (list/set), realms (list/sync), tfa
  - `pc sdn` — zones/vnets/controllers (list) + create-zone/create-vnet
  - `pc pbs` — backup-server remotes, status, datastores/namespaces/snapshots
  - `pc subscription` — node-status, keys (list/show/add/remove)
  - `pc server` — auth realms (ad/ldap/openid CRUD), acme, certificate, notes,
    views, and `server node` (PDM appliance host: status/time/dns/network/apt/…)
  - `pc resources` — aggregate status/top-entities/subscription/location-info
  - `pc auto-install` — installations/prepared/tokens (list + delete)
  - `pc remote` gained per-remote PVE reads: resources, options, next-id,
    updates-list, firewall, node-storage/status/network
- Coverage: curated **PDM 45→138 (43%)**, PVE 60→80. Remaining ~180 PDM
  endpoints (deep node/appliance admin, acme/cert writes, openid/tfa flows,
  metric-collection, niche probes) stay reachable via `pc raw` / `pc api`.
- Live-verified the read surface against PDM 1.1 (ceph across 3 clusters,
  access users/roles/realms, sdn zones, subscription node-status, per-remote
  resources/storage, appliance host status). Writes are confirm-gated but not
  run against production PDM.

## [0.7.0] — broad PDM curation (snapshot, migrate, monitoring, remote mgmt)
- **Added**: guest operations are now provider-aware — the same commands work on
  PVE (by node) and PDM (proxied via `/pve/remotes/{remote}`). New `--remote`
  on snapshot/migrate.
  - `vm/ct snapshot rollback`; `snapshot create/list/delete` now work over PDM.
  - `vm/ct migrate` works over PDM (one of PDM's supported operations).
  - `vm/ct status`, `pending`, `rrddata`, `firewall rules|options` (read-only).
- **Added**: PDM remote management — `pc remote add/update/remove`,
  `remote cluster-status <id>`, `remote updates` (per-node update summary).
- **Fixed**: `rawMutate` now recognises the PDM-proxied task id form
  (`pve:<remote>!UPID:...`) returned by snapshot/migrate, so `--wait` actually
  polls the proxied task instead of silently returning. Found via tests, then
  confirmed live (snapshot create polls `/pve/remotes/{remote}/tasks/…/status`).
- Coverage: curated PVE 48→**60**, PDM 20→**45**. The remaining PDM endpoints
  (ceph, sdn, subscriptions, pbs, certificates, access admin, node config) stay
  reachable via `pc raw` / `pc api` by design.

## [0.6.1] — live-hardening of the PDM surface (every PDM-supported op verified)
- **Fixed**: `pc --provider pdm vm show` returned `400 'state' parameter is
  missing` — PDM's proxied config endpoint requires the mandatory `state=active`
  enum (unlike the direct PVE API). Found via live testing against PDM 1.1.
- **Changed**: `pc vm/ct console` now fails fast under token auth with actionable
  guidance — Proxmox rejects API tokens on the console websocket (the upgrade
  succeeds then the server closes with 1006); ticket auth is required. Verified
  live: console works end-to-end with ticket auth, token auth always closes.
- **Added**: `--remote` flag on `vm/ct show`, the power verbs, and `config` to
  disambiguate a vmid that exists on multiple PDM remotes (previously such a vmid
  errored with a conflict and could only be reached via `pc raw`); the conflict
  message now points at `--remote`. Verified live against PDM (vmid 100 shared by
  MP01/MP02/SDC). `--remote` errors clearly on the PVE provider.
- **Added**: `vm/ct suspend` and `resume` power verbs (previously unreachable —
  the PDM provider advertised `resume` but no command invoked it). `resume` is
  PDM-proxied; `suspend` is PVE-only and refused cleanly on PDM. Verified live.
- **Fixed**: `pc --provider pdm task show/wait/log` now accept the prefixed task
  id PDM emits (`pve:<remote>!UPID:...`) — previously rejected as "not a UPID",
  so a `--no-wait` PDM action's printed id could not be fed back in. The remote
  is parsed from the prefix; the full id is used in the PDM task path.
- **Known limitation**: PDM `guest list` status reflects PDM's cached resource
  view and can lag a power action, and a freshly-created guest is not visible to
  PDM until its next poll (use `--node` to act before then). The action itself
  completes — verified against the cluster directly.

## [0.6.0] — coverage matrix, PDM lifecycle verbs, serial console
- **Added**: API coverage matrix — `internal/coverage` registry + `make coverage`
  → `docs/coverage.md` (curated vs raw-only per provider; PVE 44/675, PDM 19/318).
- **Added**: PDM curated lifecycle via the proxy — `pc --provider pdm vm
  start/stop/shutdown/show` and task polling (`/pve/remotes/{remote}/…`);
  provisioning verbs (create/clone/delete/migrate/config --set) refuse cleanly
  on PDM.
- **Added**: `pc vm/ct console [--serial N]` — interactive serial console over a
  websocket (termproxy → vncwebsocket), raw-TTY bridge, Ctrl-] to detach
  (PVE provider; requires a TTY). New deps: gorilla/websocket, x/term (pinned to
  keep the go 1.22 floor).
- Architect-reviewed (THOROUGH); fixed the coverage `create` mis-classification,
  added the ws `binary` subprotocol, and ordered console teardown
  (Close before TTY restore).

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

[Unreleased]: https://github.com/ciroiriarte/pve-cli/compare/v0.8.0...HEAD
[0.8.0]: https://github.com/ciroiriarte/pve-cli/releases/tag/v0.8.0
[0.7.0]: https://github.com/ciroiriarte/pve-cli/releases/tag/v0.7.0
[0.6.1]: https://github.com/ciroiriarte/pve-cli/releases/tag/v0.6.1
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
