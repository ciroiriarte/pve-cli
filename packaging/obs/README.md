# OBS packaging for pve-cli

Native `.deb`/`.rpm` packages built and hosted on [build.opensuse.org](https://build.opensuse.org)
in project **`home:ciriarte:pve-cli`**. The package is named **`pve-cli`**; the
installed command is **`pc`**.

## Files

- `_service` — OBS source service: pulls the tagged release from GitHub, derives
  the version from the tag, and runs `go_modules` to vendor Go deps so the
  network-isolated OBS build is fully offline.
- `pve-cli.spec` — RPM build (openSUSE Leap/Slowroll, Rocky Linux).
- `debian/` — Debian/Ubuntu build (`dh`, offline via vendored modules).

Both builds compile `./cmd/pc`, run `./cmd/gendocs` for man pages + completions,
and install the binary, `pc*.1` man pages, and bash/zsh/fish completions.

## Target matrix

| Distro | OBS repository name | Path project | Package |
|---|---|---|---|
| Debian 13 | `Debian_13` | `Debian:13` | `.deb` |
| Ubuntu 24.04 | `xUbuntu_24.04` | `Ubuntu:24.04` | `.deb` |
| Rocky Linux 9 / 10 | `Rocky_9` / `Rocky_10` | `RockyLinux:9` / `RockyLinux:10` | `.rpm` |
| openSUSE Leap 15.6 / 16.0 | `openSUSE_Leap_15.6` / `_16.0` | `devel:languages:go` + `openSUSE:Leap:*` | `.rpm` |
| openSUSE Slowroll | `openSUSE_Slowroll` | `openSUSE:Slowroll` | `.rpm` |
| openSUSE Tumbleweed | `openSUSE_Tumbleweed` | `openSUSE:Factory` | `.rpm` |

All build for x86_64 + aarch64 (Slowroll x86_64 only). The Leap repos layer
`devel:languages:go` first so a recent Go toolchain is available.

**Excluded:** Debian 12 and Ubuntu 22.04 — stock Go is older than the required
1.22 and there is no Go side-repo for deb. (Rocky uses `golang` from base;
openSUSE uses `devel:languages:go`.)

End-user install commands per distro: see [`../../docs/install.md`](../../docs/install.md).

## One-time setup (maintainer)

```sh
osc co home:ciriarte pve-cli                 # or: osc mkpac
cp packaging/obs/_service packaging/obs/pve-cli.spec  <package-dir>/
cp -r packaging/obs/debian                            <package-dir>/
cd <package-dir> && osc add * && osc commit -m "pve-cli <version>"
```

OBS runs `_service` on commit (fetching the tag + vendoring modules) and builds
every enabled repository.

## Install (users)

```sh
# openSUSE Leap 15.6 (adjust the distro path from the matrix above)
zypper addrepo https://download.opensuse.org/repositories/home:/ciriarte:/pve-cli/openSUSE_Leap_15.6/home:ciriarte:pve-cli.repo
zypper refresh && zypper install pve-cli

# Debian 12
echo 'deb http://download.opensuse.org/repositories/home:/ciriarte:/pve-cli/Debian_12/ /' | sudo tee /etc/apt/sources.list.d/pve-cli.list
curl -fsSL https://download.opensuse.org/repositories/home:/ciriarte:/pve-cli/Debian_12/Release.key | gpg --dearmor | sudo tee /usr/share/keyrings/pve-cli.gpg >/dev/null
sudo apt update && sudo apt install pve-cli
```

Then run `pc --help`.

## Notes / TODO

- **License**: Apache-2.0 (`LICENSE` + `NOTICE` at the repo root); the spec ships
  `%license LICENSE` and `debian/copyright` references `/usr/share/common-licenses/Apache-2.0`.
- The Debian `golang-go (>= 2:1.22~)` build-dep requires the distro to ship Go
  ≥ 1.22 (true for the targeted releases). For older bases, switch to the OBS
  `go1.x` images.
- Bump `_service` `revision` and `debian/changelog` on each release.

## Go floor (`go 1.22.0`) and the golang.org/x/net pin

`go.mod` stays at `go 1.22.0` and `golang.org/x/net` at v0.35.0 (#46). This is
a deliberate hold, not a hard limit: every target can reach a newer Go, but only
after OBS project-meta changes (below).

### What each target resolves today vs. what is reachable

Checked 2026-11 with `osc buildinfo` and by listing the repos' published packages.

| Target | Resolved today (current repo paths) | Reachable by adding repo paths |
|---|---|---|
| Debian 13 | golang-go 1.24.4 (`Debian:13/standard`) | `Debian:13/backports`: `golang-1.25-go`, `golang-1.26-go`, `golang-1.27-go` (x86_64 and aarch64) |
| Ubuntu 24.04 | golang-go 1.22.2 (`Ubuntu:24.04/standard`) | `Ubuntu:24.04/universe-update`: `golang-1.25-go` 1.25.14 and `golang-1.26-go` 1.26.8 (x86_64 and aarch64); 1.23/1.24 also appear there |
| Leap 15.6 | go1.22.2 (`openSUSE:Leap:15.6/standard`; the other path entry is `deleted`) | `devel:languages:go/SLE_15_SP6`: `go1.23` to `go1.27` (x86_64 and aarch64) |
| Leap 16.0, Tumbleweed, Slowroll, Rocky 9/10 | Go 1.27 | already there |

The highest floor every target can reach with those paths added is **Go 1.26**
(limited by Ubuntu 24.04; Debian 13 and Leap 15.6 reach 1.27).

### x/net: what a higher floor fixes

`go` directive required by x/net: v0.35 needs 1.18, v0.36-v0.43 need 1.23,
v0.44-v0.50 need 1.24, v0.51-v0.58 need 1.25, v0.59-v0.63 need 1.26.

| Advisory | Fixed in | Needs Go |
|---|---|---|
| GO-2025-3503 (`x/net/proxy` `PerHost`) | v0.36.0 | 1.23 |
| GO-2026-5942 (`x/net/dns/dnsmessage`) | v0.56.0 | 1.25 |
| GO-2026-6866, GO-2026-6860 | v0.62.0 | 1.26 |

A higher floor fixes some advisories, not all: 1.23/1.24 clears GO-2025-3503 only,
1.25 adds GO-2026-5942, and 1.26 is needed for the rest. govulncheck currently
reports none of the x/net findings as reachable from `pc`.

### Path to raising the floor (maintainer decision; edits the OBS project)

1. Pick the floor. 1.26 clears all the advisories above; 1.25 is the smaller step.
2. Edit the project meta (`osc meta prj -e home:ciriarte:pve-cli`):
   - `xUbuntu_24.04`: add `<path project="Ubuntu:24.04" repository="universe-update"/>`.
   - `Debian_13`: add `<path project="Debian:13" repository="backports"/>`.
   - `openSUSE_Leap_15.6`: replace the `deleted` path with
     `<path project="devel:languages:go" repository="SLE_15_SP6"/>`.
3. Packaging:
   - `debian/control` and `pve-cli.dsc`: `Build-Depends: ..., golang-1.26-go`
     (replace `golang-go (>= 2:1.22~)`); in `debian/rules` add
     `export PATH := /usr/lib/go-1.26/bin:$(PATH)`.
   - `pve-cli.spec`: on SUSE `BuildRequires: go1.26` (replace `go >= 1.22`);
     the Rocky branch keeps `golang >= 1.26`.
4. `go.mod`: raise the `go` directive, bump `golang.org/x/net` to the newest
   version compatible with the floor, `go mod vendor`, run govulncheck, and
   build with `GOTOOLCHAIN=go1.26.x`.
5. Verify with `osc buildinfo <repo> <arch> <spec|dsc>` for every repository in
   the matrix before committing to OBS.

Not yet tested: the Debian and Leap path additions and the Build-Depends/PATH
changes were checked only by listing the repos' packages, not built.

### Re-check

Re-run the `osc buildinfo` checks above on each minor release (as part of the
release step) and revisit the floor if the results change.

Proxy reachability is separate. GO-2025-3503 becomes reachable if code calls
the x/net symbols `proxy.Dial`, `proxy.FromEnvironment`,
`proxy.FromEnvironmentUsing`, `PerHost.*`, or httpproxy `config.useProxy`.
gorilla/websocket uses `proxy.FromURL`, which is not among them, and
`http.ProxyFromEnvironment` is the stdlib half of that advisory, fixed by the
toolchain. CI runs govulncheck on every push and fails on reachable findings.
