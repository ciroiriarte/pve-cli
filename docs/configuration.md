# Configuration & authentication

`pc` reads settings from (highest priority first):

1. **command-line flags** (e.g. `--server`, `--token-id`)
2. **environment variables** (`PVE_CLI_*`)
3. the **context**'s selected profile
4. the **profile**'s `defaults`
5. built-in defaults

So a flag always wins; an env var overrides the config file; the file is the
durable fallback.

## Quick setup

```bash
pc auth login https://pve1.example:8006 --token-id 'svc@pve!cli'
```

This prompts for the token secret, stores it in your OS keyring, writes a
profile + context, and makes it current. Inspect or edit afterwards:

```bash
pc config view            # secrets redacted
pc config path            # where the file lives
pc context list           # configured contexts (current marked with *)
pc context use corp       # switch cluster
```

## Config file

Default location: `${XDG_CONFIG_HOME:-~/.config}/pve-cli/config.yaml`
(override with `PVE_CLI_CONFIG`). kubeconfig-style:

```yaml
current_context: home
contexts:
  home: { profile: homelab }
  corp: { profile: corp-pdm, remote: dc-west }   # 'remote' applies to PDM only
profiles:
  homelab:
    provider: pve                                 # pve | pdm
    server: https://pve1.example:8006
    auth:
      type: token                                 # token | ticket
      token_id: "svc@pve!cli"
      secret_ref: "keyring://pve-cli/homelab"     # see "Secrets" below
    tls:
      fingerprint: "sha256:AB:CD:..."             # pin self-signed certs
      verify: true
    rate_limit:                                   # optional client-side throttle
      qps: 10
      burst: 20
    defaults:
      output: table
  corp-pdm:
    provider: pdm
    server: https://pdm.example:443
    auth: { type: token, token_id: "svc@pdm!cli", secret_ref: "keyring://pve-cli/corp-pdm" }
    tls:  { ca_file: "/etc/ssl/certs/pdm-ca.pem" }
```

`pc config set` accepts dotted keys and coerces scalar types
(`pc config set profiles.homelab.rate_limit.qps 10`).

## Authentication

### API token (recommended)

Non-interactive and CSRF-free. Create one in **Datacenter → Permissions → API
Tokens**. Provide `token_id` (`user@realm!tokenname`) + secret.

### Ticket (user + password)

`pc` logs in via `/access/ticket`, caches the ticket (~2 h) and sends the CSRF
token on writes automatically.

```yaml
auth: { type: ticket, user: "root@pam", secret_ref: "keyring://pve-cli/home" }
```

The password is the profile "secret" (keyring/env/inline, as below). Provide it
non-interactively with `PVE_CLI_PASSWORD`.

## Secrets

The `secret_ref` field dereferences a secret instead of storing it in plaintext:

- `keyring://<service>/<key>` — OS keyring (Secret Service / Keychain / WinCred).
  `pc auth login` writes here by default.
- `env:NAME` — read from environment variable `NAME`.

A plaintext `secret:` is supported but warns on use. Headless machines without a
keyring fall back to env vars or an explicit `secret:`.

## Environment variables

| Variable | Overrides |
|---|---|
| `PVE_CLI_SERVER` | API base URL |
| `PVE_CLI_TOKEN_ID` / `PVE_CLI_TOKEN_SECRET` | token auth |
| `PVE_CLI_USER` / `PVE_CLI_PASSWORD` | ticket auth |
| `PVE_CLI_TLS_FINGERPRINT` | pinned SHA-256 fingerprint |
| `PVE_CLI_INSECURE` | disable TLS verification (`true`/`false`) |
| `PVE_CLI_OUTPUT` | default output format |
| `PVE_CLI_PROFILE` / `PVE_CLI_CONTEXT` | select profile/context |
| `PVE_CLI_CONFIG` | config file path |

## TLS

In order of precedence: system CA → profile `ca_file` / `--cacert` →
**SHA-256 fingerprint pinning** (`tls.fingerprint` / `--tls-fingerprint` /
`PVE_CLI_TLS_FINGERPRINT`) → `--insecure` (opt-in, disables verification).
Fingerprint pinning is the recommended way to trust a self-signed cluster.

Get a node's fingerprint:

```bash
ssh root@node "openssl x509 -in /etc/pve/local/pve-ssl.pem -noout -fingerprint -sha256"
```

## Selecting the backend (PVE vs PDM)

Set `provider:` in the profile, or override per-invocation with `--provider
pve|pdm`. See [providers.md](providers.md) for what differs between them.
