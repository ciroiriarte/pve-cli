# pve-cli

A remote-first, [OpenStack-Client](https://docs.openstack.org/python-openstackclient/latest/)-inspired
command-line client for **Proxmox VE** and **Proxmox Datacenter Manager (PDM)**.
The installed command is **`pc`**.

`pc` talks to the Proxmox REST API over HTTPS, so **nothing needs to be installed
on the cluster nodes**. It gives you one consistent `pc <resource> <action>`
grammar — and automatic node resolution — instead of SSHing into a node to run
`qm`, `pct`, and `pvesh`.

> ⚠️ **Unofficial.** pve-cli is a community tool, **not affiliated with or
> endorsed by Proxmox Server Solutions GmbH**. "Proxmox", "Proxmox VE", and
> "Proxmox Datacenter Manager" are trademarks of Proxmox Server Solutions GmbH,
> used here only to describe interoperability. See [`NOTICE`](NOTICE).

## Highlights

- **Remote-first** — pure REST client; node-centric API paths are hidden (type
  `pc vm start 100`, not `… --node pve-01`).
- **Curated commands** for everyday work — guest lifecycle & disks, cloud-init,
  guest agent, snapshots, migrate, console, nodes, storage, backups, pools, HA,
  firewall, SDN, Ceph, and access — plus PDM fleet management. Recipes in the
  [cookbook](docs/cookbook.md).
- **Full API coverage** via schema-driven [`pc raw`](#full-api-coverage-pc-raw)
  and the `pc api` escape hatch — every endpoint is reachable.
- **Two backends, one CLI** — a single PVE cluster, or a PDM fleet (`--provider pdm`)
  with cross-remote listing. See [docs/providers.md](docs/providers.md).
- **Async-aware** — long-running operations surface their task (`--wait` /
  `--no-wait`, `pc task wait|log --follow`).
- **Auth**: API tokens or ticket (user+password); secrets in the OS keyring.
- **Scriptable**: stable `json`/`yaml` output, column selection, deterministic
  exit codes.
- **Extensible**: drop a `pc-<name>` executable on `$PATH` and call it as `pc <name>`.

## Status

Active development (`v0.10.x`, SemVer). **Both backends are live-verified**:
direct PVE against PVE 9.1/9.2 (token *and* ticket auth), and the PDM backend
against a live PDM 1.x fleet. Curated coverage is broad — daily-driver PVE
(lifecycle + disks, cloud-init, guest agent, snapshots, migrate, console,
storage, pools, HA, firewall, SDN, Ceph, access) and the PDM control plane
(remotes, cross-cluster monitoring, Ceph/SDN/PBS/subscription/server domains);
everything else is reachable via `pc raw` / `pc api`. Released binaries and
`.deb`/`.rpm` packages are published on each tag. **`v1.0.0` is gated** on the
full live test battery (see [CONTRIBUTING.md](CONTRIBUTING.md#versioning--the-v100-gate)).

## Install

### Packages (build.opensuse.org)

Native `.deb`/`.rpm` packages are built in OBS project **`home:ciriarte:pve-cli`**
for Debian 13, Ubuntu 24.04, Rocky Linux 9/10, and openSUSE Leap 15.6/16.0,
Slowroll, and Tumbleweed (x86_64 + aarch64). The package is `pve-cli`; the
command is `pc`.

**See [docs/install.md](docs/install.md) for exact per-distribution commands.**
Quick example (openSUSE):

```bash
sudo zypper addrepo https://download.opensuse.org/repositories/home:/ciriarte:/pve-cli/openSUSE_Leap_15.6/home:ciriarte:pve-cli.repo
sudo zypper --gpg-auto-import-keys refresh
sudo zypper install pve-cli
```

> Debian 12 and Ubuntu 22.04 are not packaged (their stock Go < 1.22); build
> [from source](#from-source) there.

### From source

Requires Go ≥ 1.22.

```bash
make build      # produces ./pc   (or: go build -o pc ./cmd/pc)
```

## Quickstart — 5 minutes to your first call

Create an API token in the Proxmox UI (**Datacenter → Permissions → API
Tokens**), then either store it as a profile:

```bash
pc auth login https://pve1.example:8006 --token-id 'svc@pve!cli'
# prompts for the secret, stores it in your OS keyring, sets the default context
pc node list
```

…or drive it entirely from the environment (great for CI):

```bash
export PVE_CLI_SERVER='https://pve1.example:8006'
export PVE_CLI_TOKEN_ID='svc@pve!cli'
export PVE_CLI_TOKEN_SECRET='xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx'
# Self-signed cluster? Pin the cert fingerprint instead of disabling TLS:
export PVE_CLI_TLS_FINGERPRINT='sha256:AB:CD:...'      # or --insecure (footgun)

pc vm list
pc vm start 100         # auto-resolves the hosting node, waits for the task
pc vm list -o json | jq '.[] | select(.status=="running") | .vmid'
```

## Commands

```
pc vm      list show status pending rrddata create clone migrate config snapshot
           resize move-disk unlink template cloudinit agent console
           start stop shutdown reboot suspend resume reset delete
pc ct      … (same verbs for LXC containers; move-volume instead of move-disk; alias: container)
pc guest   list show status | start stop shutdown reboot suspend resume console
           # unified VM + container view; lifecycle auto-detects VM vs CT by vmid
pc node    list show | service apt network subscription
pc storage list show status | content list·delete | prune-backups
pc backup  create list | job list·show·create·delete
pc pool    list show create update delete
pc ha      status | resource list·show·add·remove | groups
pc firewall rules aliases ipset options groups macros      # scope: --node / --vmid
pc sdn     zone vnet subnet controller (list·show·create·delete) | ipams dns apply
pc ceph    health osd pool service config                  # PVE (--node) management
pc access  user token role acl realm group                 # users, tokens, ACLs, realms
pc task    list show wait log [--follow]
pc remote  list show add update remove | cluster-status | updates | node-* | resources …  # PDM
pc raw <segments...> | pc api <METHOD> <path> [-d k=v]     # full coverage / escape hatch
pc config | context | auth | plugin | completion | version
# PDM control-plane (provider: pdm): pc ceph | sdn | pbs | subscription | server | resources | auto-install
```

Run `pc <command> --help` for flags and examples; see the
**[cookbook](docs/cookbook.md)** for day-to-day recipes. A full generated
command reference (man pages + markdown) is produced by `make docs`.

The node hosting a guest is resolved automatically from `/cluster/resources`, so
`pc vm start 100` just works. Pass `--node` to override or disambiguate.

### Lifecycle example

```bash
pc vm clone 9001 200 --name web-01 --full --wait
pc vm start 200 --wait
pc vm config 200 --set cores=4 --set memory=4096
pc vm snapshot create 200 pre-upgrade
pc vm delete 200 --yes          # destructive ops prompt unless --yes/--force
```

### Full API coverage (`pc raw`)

When a curated command doesn't exist yet, walk the embedded API schema:

```bash
pc raw                                   # list top-level segments
pc raw nodes pve-01 qemu 100 status current
pc raw nodes pve-01 qemu 100 config --method POST -d cores=4
pc raw nodes pve-01 qemu --help          # describe methods + parameters
```

## Output & scripting

`json`/`yaml` are the stable scripting contract; the human table layout is **not**
guaranteed stable across versions.

```bash
pc vm list                                  # table (default)
pc vm list -o json                          # machine-readable
pc vm list -c vmid -c name -o value         # headerless columns for awk/xargs
pc vm list --status running -c vmid -o value | xargs -n1 pc vm stop --yes
```

Exit codes: `0` ok · `1` generic · `2` auth/config · `3` not found ·
`4` API/server · `5` task failed · `6` validation.

## Configuration & auth

Settings resolve in this order: **flag → env var → context → profile defaults →
built-in**. Token and ticket auth are supported; secrets live in the OS keyring.
Full details, the config-file schema, all environment variables, and PDM setup
are in **[docs/configuration.md](docs/configuration.md)**.

## Proxmox VE vs PDM

The same grammar targets a single cluster (`provider: pve`) or a PDM fleet
(`provider: pdm` / `--provider pdm`). Capabilities differ per backend (e.g.
`pc remote` is PDM-only); the matrix is in **[docs/providers.md](docs/providers.md)**.

## Rosetta Stone

| Native Proxmox | pve-cli |
|---|---|
| `qm start 100` (on the node) | `pc vm start 100` |
| `pct list` | `pc ct list` |
| `pvesh get /nodes/pve1/qemu` | `pc vm list --node pve1` |
| `pvesh create /nodes/pve1/qemu/100/clone -newid 200` | `pc vm clone 100 200` |
| `pvesh get /cluster/resources` | `pc api GET /cluster/resources` |

(`pvesh` runs locally on a node as root; `pc` runs anywhere and authenticates as
any token/user — see [docs/configuration.md](docs/configuration.md).)

## Documentation

- [docs/cookbook.md](docs/cookbook.md) — day-to-day recipes (lifecycle, snapshots, agent, firewall, SDN, Ceph, PDM, reports)
- [docs/install.md](docs/install.md) — per-distribution package install commands
- [docs/configuration.md](docs/configuration.md) — auth, profiles, contexts, env vars, TLS
- [docs/providers.md](docs/providers.md) — PVE vs PDM capability matrix
- [docs/coverage.md](docs/coverage.md) — curated-vs-raw endpoint coverage per provider
- [CONTRIBUTING.md](CONTRIBUTING.md) — build, test, project layout, releases, the v1.0.0 gate
- [CHANGELOG.md](CHANGELOG.md) — release history
- `make docs` — generates man pages, shell completions, and a full markdown command reference

## Development

```bash
make build    # produces ./pc
make test     # unit + httptest integration tests
make vet
make docs     # man pages + completions + markdown reference into dist/
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the architecture and how to add commands.

## License

[Apache-2.0](LICENSE) — see [`NOTICE`](NOTICE). Using the Proxmox REST API imposes
no licensing obligation on this client (Proxmox confirms API access is not
derivative work), so this choice is independent of Proxmox VE's own AGPLv3.
