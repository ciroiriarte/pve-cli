# Backends: Proxmox VE vs Proxmox Datacenter Manager

`pc` speaks to two kinds of endpoint behind the **same command grammar**:

- **`pve`** — a single Proxmox VE cluster (`https://node:8006`).
- **`pdm`** — Proxmox Datacenter Manager, which aggregates many clusters
  ("remotes") behind one endpoint.

Select the backend with `provider:` in the profile or `--provider pve|pdm`.
Commands that a backend doesn't support fail fast with a clear message rather
than guessing.

> **Status:** the PVE backend is verified against PVE 9.x. The PDM backend is
> implemented and unit/fixture-tested but **not yet verified against a live PDM
> instance**; treat PDM support as experimental.

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
| `remote …` (list/show/add/update/remove, cluster-status, updates, per-remote reads) | ❌ | ✅ | PDM-only; refused on PVE |
| `ceph`, `sdn`, `pbs`, `subscription`, `server`, `resources`, `auto-install` | ❌ | ✅ | PDM control-plane domains; refused on PVE. Reads + confirm-gated writes |
| `console` | ✅ | ❌ | PVE only, ticket auth required (Proxmox rejects tokens on the console websocket) |
| `raw` | ✅ | ✅ | walks the backend's own embedded schema |
| `api` | ✅ | ✅ | raw passthrough to either API |
| `config`/`context`/`auth`/`plugin`/`version`/`completion` | ✅ | ✅ | local, backend-independent |

✅ supported · ➖ reachable via `pc raw`/`pc api` (not yet a curated command) · ❌ not applicable/unsupported by the backend

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

Curated per-remote operations (use `--remote` when a vmid exists on more than
one remote):

```bash
pc --provider pdm vm start 100 --remote dc-west
pc --provider pdm vm snapshot create 100 pre-upgrade --remote dc-west
pc --provider pdm vm migrate 100 --target-node node-2 --remote dc-west --online
pc --provider pdm remote cluster-status dc-west
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
