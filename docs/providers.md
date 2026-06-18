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
| `vm`/`ct show` | ✅ | ➖ | PDM: use `pc raw` against the remote (see below) |
| guest lifecycle (create/clone/migrate/config/snapshot/power/delete) | ✅ | ➖ | PDM proxied lifecycle not wrapped yet — use `pc raw pve remotes <remote> …` |
| `storage`, `backup` | ✅ | ➖ | PVE-scoped today |
| `task list/show/wait/log` | ✅ | ➖ | PVE task surface |
| `remote list/show` | ❌ | ✅ | PDM-only; refused on PVE |
| `raw` | ✅ | ✅ | walks the backend's own embedded schema |
| `api` | ✅ | ✅ | raw passthrough to either API |
| `config`/`context`/`auth`/`plugin`/`version`/`completion` | ✅ | ✅ | local, backend-independent |

✅ supported · ➖ reachable via `pc raw`/`pc api` (not yet a curated command) · ❌ not applicable

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

Until proxied lifecycle is wrapped, drive per-remote operations through the
escape hatch, which reaches PDM's proxied PVE endpoints:

```bash
pc --provider pdm raw pve remotes dc-west qemu 100 status start --method POST
```

## Why a hard provider boundary

PDM is a higher-level control plane (remotes, cross-cluster views), not a
transparent proxy for a single cluster. Modeling it as a distinct provider —
rather than pretending one resource model fits both — keeps semantics honest:
shared transport/auth/TLS, but backend-specific commands and behavior, gated by
each provider's declared capabilities.
