# Changelog

All notable changes to pve-cli. Format based on [Keep a Changelog](https://keepachangelog.com/);
versioning is [SemVer](https://semver.org). While on `0.x`, the CLI/config/`json`
surface may change between minor releases.

## [Unreleased]

## [0.11.0] — UX review: command clarity, safety, and onboarding

A sweep driven by a multi-model UX/interface review of the command surface
(16 findings, all live-verified — PVE on a 9.1 node, PDM against a live fleet).

- **Added**: `pc guest` is no longer list-only — it gained type-agnostic
  lifecycle verbs (`show`, `status`, `start`, `stop`, `shutdown`, `reboot`,
  `suspend`, `resume`, `console`). Because Proxmox shares one id namespace, a
  vmid is unambiguous, so `pc guest start 100` resolves whether 100 is a VM or a
  container and routes accordingly — no need to know the type up front.
- **Added**: trust-on-first-use TLS pinning in `pc auth login` — an untrusted
  (self-signed) server cert is shown by SHA-256 fingerprint and pinned into the
  profile on confirmation, replacing the manual `openssl`-over-SSH dance.
- **Added**: ticket (user/password) auth via `pc auth login --user <user@realm>`
  (mutually exclusive with `--token-id`); the password is stored in the keyring.
- **Added**: `pc config test-auth` — diagnoses credential resolution, keyring
  reachability, and connectivity (one authenticated request; `--offline` to skip)
  for the opaque auth failures common on headless hosts.
- **Added**: first-class flags on create commands — `backup job create`
  (`--storage/--schedule/--mode/--vmid/--all/--enabled`) and `sdn`
  (`--bridge/--tag/--alias/--gateway/--snat`); `--set key=value` stays as the
  escape hatch and still overrides.
- **Added**: `--help` now shows an `Active backend: <provider> (context, server)`
  footer, and a one-time shell-completion tip prints after `pc auth login`.
- **Changed**: `pc vm/ct show` is now a full snapshot (config **merged with**
  live status); `config` is the raw config only (`--set` to modify); `status` is
  runtime fields only — three previously-overlapping commands now have distinct
  roles.
- **Changed**: hierarchical noun-verb commands for the PDM subtrees —
  `remote cluster status`, `remote node status|network|storage`,
  `remote updates list`, `auto-install prepared show|delete`,
  `resources location info`. The old hyphenated names keep working as hidden
  aliases.
- **Changed**: `delete` is the canonical destructive verb (`pc remote delete`;
  `remove`/`rm` remain as aliases).
- **Changed**: `--node` is now optional on cluster-wide reads (`ceph
  health/osd list/pool list/config`, `storage status`, `backup list`) — they
  auto-resolve an online node. Writes still require an explicit `--node`.
- **Changed**: the table-only flags (`--column`/`--sort`/`--no-headers`) are
  hidden from help on non-tabular commands (they still work everywhere); only
  `--format` is shown universally.
- **Changed**: provider-mismatch errors now name the active provider.
- **Fixed**: `pc vm/ct show|status|config -o json` emitted an array of
  `{key,value}` pairs instead of a native object, so `jq '.cores'` failed. They
  now emit a native object, restoring the documented json scripting contract.
- **Fixed/Safety**: previously-ungated disruptive operations now confirm
  (respecting `-y/--yes`): `sdn apply`, `ceph osd in/out`, and any non-GET method
  on the `raw`/`api` escape hatches (a buried `--method DELETE` no longer mutates
  silently).

> **0.x surface change:** the json-shape fix and the `show`/`config`/`status`
> role split change those commands' output — scripts should target the
> documented `json`/`yaml` contract.

## [0.10.3] — PDM proxy-boundary guard + docs refresh
- **Fixed**: guest extras that PDM's proxy doesn't expose (agent, cloud-init,
  resize, move-disk/volume, unlink, sendkey, template, reset) now refuse cleanly
  on the PDM provider instead of surfacing a raw `404 Path not found`. (PDM
  proxies status/config/snapshot/migrate/pending/rrddata/firewall/power only —
  confirmed live; the guest agent is not proxied, so cross-cluster OS/IP detail
  needs a direct PVE connection.)
- **Docs**: added [docs/cookbook.md](docs/cookbook.md) — day-to-day recipes
  (lifecycle, disks/cloud-init, snapshots/backups, agent, storage/pools/HA,
  firewall/SDN/Ceph, PDM fleet, and report scripts) to keep the README light.
  Refreshed the README (Status, Commands), `providers.md`, and `configuration.md`
  to match the current surface: both backends live-verified, the full curated
  command set, the PDM proxy boundary, and the ticket-auth-needs-a-profile note.

## [0.10.2] — release pipeline working
- **Fixed**: the `before` hook generated man/completions into `dist/` —
  goreleaser's own output dir — so `goreleaser release` aborted with
  `dist is not empty`. Generated docs now go to `gendocs-out/`. With this and
  the v0.10.1 fix, the `release` workflow finally publishes artifacts (static
  binaries + `.deb`/`.rpm` + archives) — the first working GitHub Release.

## [0.10.1] — fix the release pipeline (partial)
- **Fixed**: `.goreleaser.yaml` had an unquoted `description` containing `: `
  (`(command: pc)`), which broke `goreleaser check` (YAML parse) on every tag
  since v0.6.1. (The release still failed on a second issue — see 0.10.2.)

## [0.10.0] — Tier-2 PVE curation (SDN, firewall, Ceph management)
- **Added**: `pc sdn` is now provider-aware and much richer — zones, vnets,
  subnets, controllers (list/show/create/delete), plus `ipams`, `dns`, and
  `apply` (PVE `/cluster/sdn`; PDM uses `/sdn`).
- **Added**: `pc firewall` (new) — multi-scope via `--node`/`--vmid`/`--ct`
  (cluster, node, or guest): `rules` (list + `rule add`/`delete`), `aliases`,
  `ipset`, `options`, and cluster-scope `groups`/`macros`.
- **Added**: Ceph management under `pc ceph` (PVE, node-scoped via `--node`):
  `health`, `osd` (list/in/out/scrub/destroy), `pool` (list/create/delete),
  `service` (start/stop/restart), `config`. Coexists with the PDM monitoring
  verbs (which take a `<cluster>`).
- Coverage: curated **PVE 126→177 (26%)**. Live-verified against bigiron (PVE 9.1,
  token) and MP02 (PVE 9.2, ticket): firewall + SDN reads on both; Ceph
  health/osd/pool/config + SDN zones/vnets/subnets on MP02 (HEALTH_OK). SDN/Ceph
  writes are confirm-gated and not run against the shared cluster.

## [0.9.0] — Tier-1 PVE curation (daily-driver breadth)
- **Added**: guest extras (vm + ct) — `resize`, `template`; VM-only `reset`,
  `move-disk`, `unlink`, `sendkey`, `cloudinit` (show/dump/regenerate), and a
  `vm agent` group (ping/exec/network/osinfo/users/fstrim/shutdown/set-password);
  CT-only `move-volume`. All provider-aware (work on PVE and the PDM proxy).
- **Added**: `pc access` is now provider-agnostic (works on PVE *and* PDM where
  the `/access/*` paths match) + `access groups`.
- **Added**: `pc pool` (list/show/create/update/delete), `pc ha`
  (status, resource list/show/add/remove, groups).
- **Added**: node ops under `pc node` — `service` (list + start/stop/restart/reload),
  `apt` (versions/updates), `network`, `subscription`.
- **Added**: storage volume ops — `storage status`, `storage content delete`,
  `storage prune-backups` (dry-run list, `--apply` to delete); and scheduled
  `pc backup job` (list/show/create/delete).
- Coverage: curated **PVE 80→126 (19%)**. Live-verified against bigiron (PVE 9.1,
  token auth) and MP02 (PVE 9.2, ticket auth): reads + resize/reset/template
  writes; guest-agent path confirmed (clean "agent not running" where the image
  lacks it).

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

[Unreleased]: https://github.com/ciroiriarte/pve-cli/compare/v0.10.3...HEAD
[0.10.3]: https://github.com/ciroiriarte/pve-cli/releases/tag/v0.10.3
[0.10.2]: https://github.com/ciroiriarte/pve-cli/releases/tag/v0.10.2
[0.10.1]: https://github.com/ciroiriarte/pve-cli/releases/tag/v0.10.1
[0.10.0]: https://github.com/ciroiriarte/pve-cli/releases/tag/v0.10.0
[0.9.0]: https://github.com/ciroiriarte/pve-cli/releases/tag/v0.9.0
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
