# pve-cli

A remote-first, [OpenStack-Client](https://docs.openstack.org/python-openstackclient/latest/)-inspired command-line client for **Proxmox VE**. The binary is **`pc`**.

`pc` talks to the Proxmox REST API over HTTPS, so nothing needs to be installed on the cluster nodes. It gives you one consistent `pc <resource> <action>` grammar instead of SSHing into a node to run `qm`, `pct`, and `pvesh`.

> ⚠️ **Unofficial:** pve-cli is a community tool and is **not affiliated with or endorsed by Proxmox Server Solutions GmbH**. "Proxmox" and "Proxmox VE" are trademarks of their respective owner.

> **Status:** M1 (MVP). Direct-PVE only, API-token auth, core compute/node/task commands, and a raw `pc api` escape hatch. See [`docs`](docs/) and the implementation plan for the roadmap (generated full-API coverage, profiles/keyring, Proxmox Datacenter Manager backend, plugins).

## Install (from source)

Requires Go ≥ 1.22.

```bash
make build      # produces ./pc
# or:
go build -o pc ./cmd/pc
```

## Quickstart — 5 minutes to your first call

Create an API token in the Proxmox UI (Datacenter → Permissions → API Tokens), then:

```bash
export PVE_CLI_SERVER='https://pve1.example:8006'
export PVE_CLI_TOKEN_ID='svc@pve!cli'
export PVE_CLI_TOKEN_SECRET='xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx'

# Self-signed cluster? Pin the cert fingerprint instead of disabling TLS:
export PVE_CLI_TLS_FINGERPRINT='sha256:AB:CD:...'      # or --insecure (footgun)

pc node list
pc vm list
pc vm start 100         # auto-resolves which node hosts VM 100, waits for the task
pc vm list -o json | jq '.[] | select(.status=="running") | .vmid'
```

## Commands (M1)

```
pc node list|show
pc vm   list|show|start|stop|shutdown|reboot      # QEMU VMs
pc ct   list|show|start|stop|shutdown|reboot      # LXC containers (alias: container)
pc task list|show|wait
pc api  <METHOD> <path> [--data k=v ...]          # raw escape hatch (like `gh api`)
pc version
pc completion bash|zsh|fish|powershell
```

The node hosting a guest is resolved automatically from `/cluster/resources`, so you type `pc vm start 100`, not `pc vm start 100 --node pve-01`. Pass `--node` to override or disambiguate.

## Output & scripting

`json`/`yaml` are the stable scripting contract; the human table layout is **not** guaranteed stable.

```bash
pc vm list                                  # table (default)
pc vm list -o json                          # machine-readable
pc vm list -c vmid -c name -o value         # headerless columns for awk/xargs
pc vm list --status running -c vmid -o value | xargs -n1 pc vm stop --yes
```

Exit codes: `0` ok · `1` generic · `2` auth/config · `3` not found · `4` API/server · `5` task failed · `6` validation.

## Configuration

Flags override environment variables, which override the config file. Settings resolve in this order: **flag → env → context → profile defaults → built-in**.

Config file (`~/.config/pve-cli/config.yaml`):

```yaml
current_context: home
contexts:
  home: { profile: homelab }
profiles:
  homelab:
    provider: pve
    server: https://pve1.example:8006
    auth:
      type: token
      token_id: "svc@pve!cli"
      secret_ref: "keyring://pve-cli/homelab"   # OS keychain; avoid plaintext
    tls:
      fingerprint: "sha256:AB:CD:..."
    defaults:
      output: table
```

Environment variables: `PVE_CLI_SERVER`, `PVE_CLI_TOKEN_ID`, `PVE_CLI_TOKEN_SECRET`, `PVE_CLI_TLS_FINGERPRINT`, `PVE_CLI_INSECURE`, `PVE_CLI_OUTPUT`, `PVE_CLI_PROFILE`, `PVE_CLI_CONTEXT`, `PVE_CLI_CONFIG`.

## Rosetta Stone

| Native Proxmox | pve-cli |
|---|---|
| `qm start 100` (on the node) | `pc vm start 100` |
| `pct list` | `pc ct list` |
| `pvesh get /nodes/pve1/qemu` | `pc vm list --node pve1` |
| `pvesh get /cluster/resources` | `pc api GET /cluster/resources` |

## Install (packages)

Native `.deb`/`.rpm` packages are built on [build.opensuse.org](https://build.opensuse.org)
(project `home:ciriarte:pve-cli`) for the last two major/LTS releases of Ubuntu,
Debian, Rocky Linux, openSUSE Leap, and openSUSE Slowroll. The package is
`pve-cli`; the command is `pc`. See [`packaging/obs/`](packaging/obs/) for the
spec, Debian rules, OBS service, and per-distro install instructions.

## Development

```bash
make build    # produces ./pc
make test     # unit + httptest integration tests
make vet
make docs     # generate man pages, shell completions, and the markdown
              # command reference into dist/ (consumed by packaging)
```

## License

[Apache-2.0](LICENSE). See [`NOTICE`](NOTICE) for the trademark/unofficial-tool
notice. Using the Proxmox REST API imposes no licensing obligation on this
client (Proxmox confirms API access is not derivative work); the Apache-2.0
choice is independent of Proxmox VE's own AGPLv3 license.
