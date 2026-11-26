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

All build for x86_64 + aarch64 (Slowroll x86_64 only). The build needs Go
≥ 1.26 (`go.mod` declares `go 1.26.0`), so four repos layer an extra path in
front of the base distro (see [Go floor](#go-floor-go-1260)):

| Repository | Extra path(s) | Go used |
|---|---|---|
| `Debian_13` | `Debian:13/backports` | `golang-1.26-go` |
| `xUbuntu_24.04` | `Ubuntu:24.04/universe-update`, `Ubuntu:24.04/update` | `golang-1.26-go` |
| `openSUSE_Leap_15.6` | `devel:languages:go/SLE_15_SP6` | `go1.2x` via `go >= 1.26` |
| `openSUSE_Leap_16.0` | `devel:languages:go/16.0` | `go1.2x` via `go >= 1.26` |

**Excluded:** Debian 12 and Ubuntu 22.04 — stock Go is older than the required
1.26 and the repos are not enabled. (`Ubuntu:22.04/universe-update` does carry
`golang-1.26-go`, so Ubuntu 22.04 could be added with the same layering; not
done or tested.)

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

# Debian 13
echo 'deb http://download.opensuse.org/repositories/home:/ciriarte:/pve-cli/Debian_13/ /' | sudo tee /etc/apt/sources.list.d/pve-cli.list
curl -fsSL https://download.opensuse.org/repositories/home:/ciriarte:/pve-cli/Debian_13/Release.key | gpg --dearmor | sudo tee /usr/share/keyrings/pve-cli.gpg >/dev/null
sudo apt update && sudo apt install pve-cli
```

Then run `pc --help`.

## Notes / TODO

- **License**: Apache-2.0 (`LICENSE` + `NOTICE` at the repo root); the spec ships
  `%license LICENSE` and `debian/copyright` references `/usr/share/common-licenses/Apache-2.0`.
- The Debian build-dep is `golang-1.26-go | golang-go (>= 2:1.26~)`, and
  `debian/rules` puts `/usr/lib/go-1.26/bin` first on `PATH` (where the stock
  `golang-go` is already ≥ 1.26 that dir is absent and `/usr/bin/go` is used).
  When raising the floor again, bump both together.
- Bump `_service` `revision` and `debian/changelog` on each release.

## Go floor (`go 1.26.0`)

`go.mod` declares `go 1.26.0` with no `toolchain` line, so the offline OBS
build never tries to download a toolchain; the distro Go must be ≥ 1.26.
The floor was raised from 1.22 in #46 so `golang.org/x/net` could move from
v0.35.0 (the last release compatible with Go 1.22) to the latest (v0.63.0,
which needs Go 1.26). That cleared every x/net advisory govulncheck reported
(1 package-level, 18 module-level; plus x/sys GO-2026-5024 via x/sys v0.50.0):

| Advisory | Fixed in | Needs Go |
|---|---|---|
| GO-2025-3503 (`x/net/proxy`) | v0.36.0 | 1.23 |
| GO-2025-3595 | v0.38.0 | 1.23 |
| GO-2026-4440, GO-2026-4441 | v0.45.0 | 1.24 |
| GO-2026-4918 | v0.53.0 | 1.25 |
| GO-2026-5025, 5027, 5028, 5029, 5030 | v0.55.0 | 1.25 |
| GO-2026-5942 | v0.56.0 | 1.25 |
| GO-2026-5026 | v0.57.0 | 1.25 |
| GO-2026-6603, 6610, 6611, 6612, 6617 | v0.60.0 | 1.26 |
| GO-2026-6860, GO-2026-6866 | v0.62.0 | 1.26 |

### Go per target

Checked 2026-11 with OBS builds of the branch (build logs) and repo listings.
Without the extra paths above, Debian 13 resolves golang-go 1.24.4, Ubuntu
24.04 golang-go 1.22.2 and Leap 15.6 go1.22.2, all too old.

| Target | Go source | Highest Go reachable |
|---|---|---|
| Debian 13 | `Debian:13/backports` `golang-1.26-go` | 1.27 (`golang-1.27-go`) |
| Ubuntu 24.04 | `Ubuntu:24.04/universe-update` `golang-1.26-go` 1.26.8 | 1.26 |
| Leap 15.6 | `devel:languages:go/SLE_15_SP6` | 1.27 |
| Leap 16.0 | `devel:languages:go/16.0` | 1.27 |
| Tumbleweed, Slowroll, Rocky 9/10 | base paths | 1.27 |

Ubuntu 24.04 caps the floor at **1.26**. Raising it to 1.27 needs a
`golang-1.27-go` there first.

### Raising the floor again

1. Confirm every target ships the new Go (`osc buildinfo <repo> <arch>
   <spec|dsc>`; for deb, list `golang-1.NN-go` in the extra paths).
2. `pve-cli.spec`: `go >= 1.NN` (SUSE) and `golang >= 1.NN` (Rocky).
3. `debian/control` and `pve-cli.dsc`: `golang-1.NN-go | golang-go (>= 2:1.NN~)`;
   `debian/rules`: `/usr/lib/go-1.NN/bin` on `PATH`.
4. `go.mod`: raise the `go` directive (no `toolchain` line), bump deps,
   `go mod vendor`, build with `GOTOOLCHAIN=go1.NN.0`, run govulncheck.
5. Test on an OBS branch (`osc branch home:ciriarte:pve-cli pve-cli`, point
   `_service` `revision` at the git branch) until all repos build, before the
   release that ships it.

### Re-check

Re-run the `osc buildinfo` checks above on each minor release (as part of the
release step) and revisit the floor if the results change. CI runs govulncheck
on pull requests and on pushes to `main` and `v*` tags, and fails on reachable
findings.
