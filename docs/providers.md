# Backends: Proxmox VE, Datacenter Manager and Backup Server

`pc` speaks to three kinds of endpoint behind the **same command grammar**:

- **`pve`** — a single Proxmox VE cluster (`https://node:8006`).
- **`pdm`** — Proxmox Datacenter Manager, which aggregates many clusters
  ("remotes") behind one endpoint.
- **`pbs`** — a Proxmox Backup Server (`https://pbs:8007`; a port-less pbs URL
  defaults to 8007). See [PBS usage](#pbs-usage).

Select the backend with `provider:` in the profile or `--provider pve|pdm|pbs`.
Commands that a backend doesn't support fail fast with a clear message naming
the current provider, before any request is sent, rather than guessing.

> **Status:** all three backends are live-verified — PVE against PVE 9.1/9.2
> (token *and* ticket auth), PDM against a live PDM 1.x fleet, and PBS against a
> PBS 4.2 server (token and ticket auth, TOFU fingerprint pinning, every
> `pc datastore` read and its json shape, a GC run, a verify run, prune dry runs,
> `job list` incl. push/pull sync jobs, and `pc task` on PBS UPIDs). Note PDM's proxy only
> exposes a subset of per-guest operations (status/config/snapshot/migrate/
> pending/rrddata/firewall/power); the **guest agent, cloud-init, disk ops,
> console, and provisioning are not proxied** and are refused on PDM — target
> the cluster directly (`provider: pve`) for those.

## Capability matrix

| Command | `pve` | `pdm` | Notes |
|---|:--:|:--:|---|
| `node list/show` | ✅ | ✅ | PDM aggregates nodes across remotes (adds a `remote` column) |
| `vm`/`ct list`, `guest list` | ✅ | ✅ | PDM lists across all remotes; guest rows gain a `remote` column |
| `vm`/`ct show`, `status`, `pending`, `rrddata`, `firewall` | ✅ | ✅ | PDM proxies these to the remote; pass `--remote` if the vmid is shared |
| power (start/stop/shutdown/reboot/suspend/resume) | ✅ | ✅* | PDM supports start/stop/shutdown/resume; reboot/suspend are PVE-only and refused on PDM |
| `snapshot` (create/list/delete/rollback) | ✅ | ✅ | PDM proxied |
| `migrate` | ✅ | ✅ | one of PDM's supported operations (proxied) |
| provisioning (`create`/`clone`/`config --set`/`delete`) | ✅ | ❌ | PDM has no provisioning API — target the cluster directly (`provider: pve`) |
| guest extras (`resize`, `template`, `move-disk`/`move-volume`, `unlink`, `sendkey`, `cloudinit`, `agent`, `reset`) | ✅ | ➖ | provider-aware; `agent`/`cloudinit` are VM-only |
| `pool`, `ha`, `node service/apt/network/subscription`, `storage status/content delete/prune-backups`, `backup job` | ✅ | ➖ | PVE daily-driver breadth (Tier 1); some `/access`, `/pools` paths also resolve on PDM |
| `access` (user/group/token/role/acl/realm) | ✅ | ✅ | provider-agnostic — `/access/*` exists on both |
| `storage`, `backup` | ✅ | ➖ | PVE-scoped today |
| `task list/show/wait/log` | ✅ | ✅ | PDM task ids are `pve:<remote>!UPID:…`; the task commands accept them |
| `remote …` (list/show/add/update/delete, cluster status, node status·network·storage, updates, per-remote reads) | ❌ | ✅ | PDM-only; refused on PVE |
| `sdn` (zones/vnets/subnets/controllers/ipams/dns/apply) | ✅ | ✅ | provider-aware: PVE `/cluster/sdn`, PDM `/sdn` (smaller set) |
| `firewall` (cluster/node/guest via `--node`/`--vmid`) | ✅ | ➖ | rules/aliases/ipset/options/groups/macros |
| `ceph` | ✅* | ✅ | PVE: node-scoped management (`--node`: osd/pool/service/config/health). PDM: cross-cluster monitoring (`<cluster>`) |
| `pbs`, `subscription`, `server`, `resources`, `auto-install` | ❌ | ✅ | PDM control-plane domains; refused on PVE. Reads + confirm-gated writes |
| `console` | ✅ | ❌ | PVE only, ticket auth required (Proxmox rejects tokens on the console websocket) |
| `raw` | ✅ | ✅ | walks the backend's own embedded schema |
| `api` | ✅ | ✅ | raw passthrough to either API |
| `config`/`context`/`auth`/`plugin`/`version`/`completion` | ✅ | ✅ | local, backend-independent |

✅ supported · ➖ reachable via `pc raw`/`pc api` (not yet a curated command) · ❌ not applicable/unsupported by the backend

On **`pbs`**, every command in the table above is refused except `task`, `raw`,
`api` and the local `config`/`context`/`auth`/`plugin`/`version`/`completion`
commands; `pc datastore` (below) is the PBS-only tree and is refused on `pve`
and `pdm`.

## PBS usage

```bash
pc auth login https://pbs1:8007 --provider pbs --token-id 'admin@pbs!cli'   # TOFU pin on first use
pc auth login https://pbs1 --provider pbs --user root@pam                   # ticket auth; :8007 is filled in
pc config test-auth
```

Token auth sends `Authorization: PBSAPIToken=<tokenid>:<secret>`; ticket auth
logs in at `/access/ticket` and sends the `PBSAuthCookie` cookie (plus the
`CSRFPreventionToken` header on writes). Fingerprint pinning works as for PVE.

| Command | API | Notes |
|---|---|---|
| `datastore list` / `show <store>` | `/admin/datastore`, `/config/datastore/{store}` | `show` is the datastore config object |
| `datastore status [store]` | `/status/datastore-usage`, `/admin/datastore/{store}/status` | all stores' usage, or one store (`--verbose` adds counts) |
| `datastore namespace list <store>` | `…/namespace` | `--parent`, `--max-depth` |
| `datastore group list <store>` | `…/groups` | `--ns` |
| `datastore snapshot list <store>` | `…/snapshots` | `--ns`, `--group vm/100`, or `--type`/`--id` |
| `datastore gc status [store]` / `gc start <store>` | `/admin/gc`, `…/gc` | `start` is confirm-gated and returns a task |
| `datastore prune <store>` | `…/prune-datastore`, `…/prune` (with `--group`) | `--keep-*` required; confirm-gated; `--dry-run` previews without a prompt |
| `datastore verify start <store>` | `…/verify` | `--ns`, `--group`, `--backup-time`, `--ignore-verified`, `--outdated-after`; confirm-gated |
| `datastore job list` | `/admin/{prune,verify,sync}` | `--type prune,verify,sync`, `--store`; each object gains `job-type`; sync asks for `sync-direction=all` (push + pull) and falls back to pull-only on PBS < 3.3 |
| `task list/show/wait/log` | `/nodes/localhost/tasks/…` | accepts PBS UPIDs (which carry an extra task-id field) |

```bash
pc datastore status                                   # usage of every datastore
pc datastore snapshot list store1 --ns prod --group vm/100 -o json
pc datastore prune store1 --keep-daily 7 --keep-weekly 4 --dry-run
pc datastore prune store1 --keep-daily 7 --keep-weekly 4 --yes
pc datastore gc start store1 --yes --no-wait -o json | jq -r .upid | xargs pc task wait
pc raw admin datastore store1 groups                  # PBS's own embedded schema
```

`pc pbs` is a different thing: it inspects PBS *remotes through PDM*
(`provider: pdm`) and is unchanged. On `provider: pbs` it is refused with a
pointer to `pc datastore`. The two trees don't share a name because their
arguments differ (`pc pbs datastore list <remote>` vs `pc datastore list`), and
overloading one command with provider-dependent arity would make scripts and
completion ambiguous.

## PDM usage

```bash
pc --provider pdm remote list                 # clusters PDM manages
pc --provider pdm guest list                  # VMs/cts across all remotes (with 'remote' column)
pc --provider pdm node list
```

Pin a default remote in the context so everyday commands stay short:

```yaml
contexts:
  corp: { profile: corp-pdm, remote: dc-west }
```

Single-guest commands then target `dc-west` without `--remote` (an explicit
`--remote` still wins). Bulk snapshot commands (`snapshot list`/`prune`) honor
the pinned remote too (and fail with exit 4 if that remote's inventory is
unavailable); `guest list` and `node list` stay cross-remote. On PDM,
`--node` doesn't skip the lookup: the guest is still resolved and `--node` must
match its node. On `provider: pve` the context `remote:` is ignored.

If a remote's inventory can't be fetched, lists still show the healthy remotes
and print a warning on stderr, but a single-guest command without a remote
scope refuses (exit 4) — the vmid might also live on the unreachable remote.
Pass `--remote` (or pin one in the context) to target a healthy remote.

Curated per-remote operations (use `--remote` when a vmid exists on more than
one remote):

```bash
pc --provider pdm vm start 100 --remote dc-west
pc --provider pdm vm snapshot create 100 pre-upgrade --remote dc-west
pc --provider pdm vm migrate 100 --target-node node-2 --remote dc-west --online
pc --provider pdm remote cluster status dc-west
```

The rest of PDM's API (ceph, sdn, subscriptions, pbs, certificates, access
administration, node networking/dns/apt) is reachable via the escape hatch,
which walks PDM's own embedded schema:

```bash
pc --provider pdm raw                          # discover the full PDM tree
pc --provider pdm api GET /resources/status    # any endpoint directly
pc --provider pdm raw pve remotes dc-west qemu 100 status --method GET
```

## Why a hard provider boundary

PDM is a higher-level control plane (remotes, cross-cluster views), not a
transparent proxy for a single cluster. Modeling it as a distinct provider —
rather than pretending one resource model fits both — keeps semantics honest:
shared transport/auth/TLS, but backend-specific commands and behavior, gated by
each provider's declared capabilities.
