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

## Target matrix (last two major/LTS releases each)

| Distro | OBS repository names | Package |
|---|---|---|
| Ubuntu LTS | `xUbuntu_24.04`, `xUbuntu_22.04` | `.deb` |
| Debian | `Debian_13`, `Debian_12` | `.deb` |
| Rocky Linux | `Rocky_10`, `Rocky_9` | `.rpm` |
| openSUSE Leap | `openSUSE_Leap_16.0`, `openSUSE_Leap_15.6` | `.rpm` |
| openSUSE Slowroll | `openSUSE_Slowroll` | `.rpm` |

Enable these repositories in the OBS project's *Meta* / *Repositories* config.

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
