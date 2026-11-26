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

`go.mod` stays at `go 1.22.0` and `golang.org/x/net` at v0.35.0 (#46). Raising
the floor was evaluated against what each OBS target can resolve (checked with
`osc buildinfo` and the published repos, 2026-11):

| Target | Go available today | Newer Go reachable? |
|---|---|---|
| Debian 13 | golang-go 1.24.4 | none in the OBS `Debian:13` repo |
| Ubuntu 24.04 | golang-go 1.22.2 | `golang-1.23/1.24/1.25/1.26-go` exist only in `Ubuntu:24.04/universe-update`, which the OBS repo does not use; needs an OBS repo path change plus `BuildRequires: golang-1.2x-go` and `PATH` in `debian/rules` |
| Leap 15.6 | go1.22.2 | `go1.23+` not resolvable; needs `devel:languages:go/SLE_15_SP6` added to the repo paths (the current `deleted` path entry is dead) |
| Leap 16.0, Tumbleweed, Slowroll, Rocky 9/10 | 1.27 | yes |

The advisories that matter are fixed in x/net v0.56.0 (`go 1.25.0`) and v0.60.0
(`go 1.26.0`). A floor of 1.24 only reaches x/net v0.50-v0.55, which does not
clear them, so raising to 1.24 buys nothing. A floor of 1.26 would need Go 1.26
on every target, which the OBS Debian 13 repo cannot provide. govulncheck
reports none of the x/net findings as reachable from `pc`.

Re-check rule: revisit the floor when any of these changes: Debian 13 gains an
official Go >= 1.25/1.26, the OBS Ubuntu/Leap 15.6 repos are layered with the
Go side-repos above, or a Ubuntu 24.04/Leap 15.6 target is dropped. Also re-run
`govulncheck` and re-evaluate the pin whenever `internal/transport/websocket.go`
or any proxy handling changes: calling `proxy.FromEnvironment`/`PerHost` or
setting `Dialer.Proxy = http.ProxyFromEnvironment` makes the x/net/proxy
advisory (GO-2025-3503) reachable.
