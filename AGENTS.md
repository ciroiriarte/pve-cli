# AGENTS.md — working conventions for pve-cli

Guidance for agents/contributors working in this repo. Architecture, project
layout, and the release/v1.0.0 gate live in [CONTRIBUTING.md](CONTRIBUTING.md);
this file records **design decisions and conventions** that aren't obvious from
the code and must be preserved.

## Build / test / run

- Go toolchain is at `~/.local/go` (not on `PATH` by default):
  `export PATH="$HOME/.local/go/bin:$PATH"`.
- `make build` → `./pc` · `make test` (unit + httptest integration) · `make vet`
  · `make docs` (man pages, completions, markdown reference).
- Live test node **bigiron** (`https://10.2.0.210:8006`, PVE 9.1.6) drives the
  v1.0.0 live gate; token + TLS fingerprint are in the maintainer's notes. Auth
  for ad-hoc checks via `PVE_CLI_SERVER` / `PVE_CLI_TOKEN_ID` /
  `PVE_CLI_TOKEN_SECRET` / `PVE_CLI_TLS_FINGERPRINT` env vars.

## Key design decisions

### 1. Output: `json`/`yaml` is the stable scripting contract; the table is not
`internal/output.Tabular` carries **both** a human rendering (`Columns` + `Rows`,
`[][]string`) and the machine payload (`Raw any`). `Render` uses **`Raw` only**
for `json`/`yaml`; `Rows`/`Columns` drive `table`/`value`/`csv`.

- **`Raw` MUST be the native typed object**, never the row projection. A singular
  read (`vm show`, `vm status`, `vm config`, `node show`, …) emits a native JSON
  **object** (`{"cores":4,...}`) so callers can `jq '.cores'`. A list emits an
  **array of objects**.
- Do **not** set `Raw` to a `[]{key,value}` slice just because the human view is a
  key/value table — that re-introduces the bug fixed in #1 (guest detail commands
  were emitting `[{"key":...,"value":...}]`). See `guestConfigTable` in
  `internal/cli/guest.go` for the canonical pattern, and
  `TestGuestConfigTableJSONIsNativeObject` for the regression guard.
- The human table layout is explicitly **not** guaranteed stable across versions;
  `json`/`yaml` are. Keep it that way.

### 2. Destructive verbs: `delete` is canonical, always confirm-gated
- Use **`delete`** as the destructive verb across resources (`vm/ct/pool/remote
  delete`). Where an older name shipped (`remote remove`), keep it as an **alias**
  (`Aliases: []string{"remove", "rm"}`), don't break it.
- Every destructive/disruptive command **prompts by default** and accepts
  `-y/--yes`. Use the shared `confirm(a, msg)` helper.
- Confirmation prompts should **echo target context** so a typo'd ID is caught —
  e.g. `permanently delete VM 200 (web-01) on pve-01?`. Follow this when adding
  new destructive commands. (Broader policy tracked in #6.)

### 3. TLS: trust-on-first-use pinning, never silent insecure
`pc auth login` probes the server cert (`transport.ProbeServerCert`). A
system-trusted cert is used as-is; an untrusted/self-signed one is shown by
SHA-256 fingerprint and pinned into the profile **only on explicit `y`**.
Non-interactive runs never auto-pin (they print the fingerprint to re-run with
`--fingerprint`). `InsecureSkipVerify` is used **only** to fetch a cert for
display/pinning, never to carry credentials — keep it that way. Fingerprint
format is `sha256:` + uppercase colon-separated hex, matching `openssl … -sha256`.

### 4. Remote-first: hide node-centric API plumbing
The tool's promise is `pc <resource> <action>` with the hosting node resolved
automatically from `/cluster/resources`. Read-only cluster-wide commands make
`--node` *optional* via `nodeOrAuto`/`firstOnlineNode` (ceph health/osd
list/pool list/config, `storage status`, `backup list`) — they pick an online
node when one isn't given. Writes (`ceph service/osd/pool` mutations,
`prune-backups`) still require an explicit `--node`. When adding a node-scoped
read whose data is cluster-wide or on shared storage, route it through
`nodeOrAuto`.

### 5. Naming: hierarchical noun-verb over compound-hyphen leaves
Prefer `remote node status` over `remote node-status`. New commands follow the
hierarchical form via the `group`/`withSubs` helpers; legacy hyphenated spellings
stay as `hidden()` back-compat aliases. Done for `remote` (cluster/node/updates),
`auto-install` (prepared/tokens/installations), and `resources location`. Two
exceptions kept as flat leaves: `ceph osd-tree` (the name `osd` is already the
PVE node-scoped mgmt command, so it can't nest) and `resources top-entities` /
`remote next-id` (single-concept leaves with no clean hierarchical form).

### 6. Onboarding nudges go to stderr, TTY-gated
Interactive hints (e.g. the shell-completion tip after `pc auth login`) print to
**stderr** and are gated on `isTTY()` so scripted/CI output stays clean. Never
emit advisory text on stdout for machine-consumed commands.

### 7. Guest commands route by resolved kind, not the command's spec
`pc guest <verb>` is type-agnostic: `resolveGuest` looks the vmid up in
`/cluster/resources` to learn VM-vs-CT (Proxmox shares one id namespace, so a
vmid is unambiguous — no `--type` flag), and CLI path builders use the resolved
`g.Kind` via `kindEndpoint(g.Kind)` / `guestBase(p, g)` — never `spec.kind`. The
typed `vm`/`ct` trees still enforce their kind (`enforceKind`) and own
provisioning (create/clone/delete/config-write), where the type is intrinsic.
When adding a guest-scoped command, build paths from `g.Kind` so it works under
`pc guest`, `pc vm`, and `pc ct` alike.

## UX roadmap

A 2026-06-19 CCA (Claude + Codex + Antigravity) review of the v0.10.x surface is
captured as GitHub issues **#1–#16**. #1–#3 are implemented; the rest
(guest lifecycle verbs, TOFU fingerprint pinning, ticket-auth login, provider
visibility, escape-hatch consolidation, …) are the queued UX work. Check open
issues before reworking the command surface.
