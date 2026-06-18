# Installing pve-cli

Native packages are built and hosted on
[build.opensuse.org](https://build.opensuse.org) in project
**`home:ciriarte:pve-cli`**. The package is **`pve-cli`**; the installed command
is **`pc`**.

## Supported distributions

| Distribution | Arch | Format | OBS repo name |
|---|---|---|---|
| Debian 13 (Trixie) | x86_64, aarch64 | `.deb` | `Debian_13` |
| Ubuntu 24.04 LTS | x86_64, aarch64 | `.deb` | `xUbuntu_24.04` |
| Rocky Linux 9 | x86_64, aarch64 | `.rpm` | `Rocky_9` |
| Rocky Linux 10 | x86_64, aarch64 | `.rpm` | `Rocky_10` |
| openSUSE Leap 15.6 | x86_64, aarch64 | `.rpm` | `openSUSE_Leap_15.6` |
| openSUSE Leap 16.0 | x86_64, aarch64 | `.rpm` | `openSUSE_Leap_16.0` |
| openSUSE Slowroll | x86_64 | `.rpm` | `openSUSE_Slowroll` |
| openSUSE Tumbleweed | x86_64, aarch64 | `.rpm` | `openSUSE_Tumbleweed` |

> **Debian 12 / Ubuntu 22.04 (and any other distro):** there is no OBS *native*
> package because their stock Go compiler is older than the required 1.22 (and,
> unlike openSUSE, there's no Go side-repo for deb). This is a **build-time**
> limit only — the binary is statically linked and runs there fine. Use the
> [prebuilt static binary / `.deb` / `.rpm`](#github-releases-static-binary--deb--rpm)
> from GitHub Releases, or build [from source](#from-source).

In every command below, replace the **repo name** segment
(`openSUSE_Leap_15.6`, `Debian_13`, …) with the row matching your system from
the table above. The base URL is always
`https://download.opensuse.org/repositories/home:/ciriarte:/pve-cli/<repo>/`.

## openSUSE (Leap / Slowroll / Tumbleweed) — zypper

```bash
sudo zypper addrepo \
  https://download.opensuse.org/repositories/home:/ciriarte:/pve-cli/openSUSE_Leap_15.6/home:ciriarte:pve-cli.repo
sudo zypper --gpg-auto-import-keys refresh
sudo zypper install pve-cli
```

Slowroll → `openSUSE_Slowroll`; Tumbleweed → `openSUSE_Tumbleweed`;
Leap 16.0 → `openSUSE_Leap_16.0`.

## Rocky Linux / RHEL-compatible — dnf

```bash
sudo dnf install -y dnf-plugins-core   # provides 'config-manager'
sudo dnf config-manager --add-repo \
  https://download.opensuse.org/repositories/home:/ciriarte:/pve-cli/Rocky_10/home:ciriarte:pve-cli.repo
sudo dnf install -y pve-cli
```

Rocky 9 → `Rocky_9`.

## Debian / Ubuntu — apt

`apt` needs the repository's signing key stored separately and referenced with
`signed-by`:

```bash
# 1. signing key
curl -fsSL https://download.opensuse.org/repositories/home:/ciriarte:/pve-cli/Debian_13/Release.key \
  | gpg --dearmor | sudo tee /usr/share/keyrings/pve-cli.gpg > /dev/null

# 2. apt source (note the trailing " /")
echo 'deb [signed-by=/usr/share/keyrings/pve-cli.gpg] https://download.opensuse.org/repositories/home:/ciriarte:/pve-cli/Debian_13/ /' \
  | sudo tee /etc/apt/sources.list.d/pve-cli.list

# 3. install
sudo apt update && sudo apt install pve-cli
```

Ubuntu 24.04 → replace both `Debian_13` paths with `xUbuntu_24.04`.

## Verify

```bash
pc version
pc --help
man pc
```

## Upgrades

Packages upgrade through your normal package manager
(`zypper up` / `dnf upgrade` / `apt upgrade`) as new releases are published to
the OBS repository.

## GitHub Releases (static binary / .deb / .rpm)

Every tagged release publishes **statically-linked** binaries plus
distro-agnostic `.deb`/`.rpm` packages (built once on a modern toolchain via
goreleaser). These install on **any** Linux regardless of its stock Go version —
including **Debian 12, Ubuntu 22.04**, EL7-era systems, Alpine, etc. — and cover
macOS and Windows too.

Static binary (Linux/macOS/Windows, amd64/arm64):

```bash
# pick your os/arch from https://github.com/ciroiriarte/pve-cli/releases/latest
curl -fsSL https://github.com/ciroiriarte/pve-cli/releases/latest/download/pve-cli_Linux_x86_64.tar.gz \
  | tar xz pc
sudo install -m0755 pc /usr/local/bin/pc
pc version
```

Distro-agnostic package (e.g. Debian 12 / Ubuntu 22.04):

```bash
# .deb
curl -fsSLO https://github.com/ciroiriarte/pve-cli/releases/latest/download/pve-cli_amd64.deb
sudo apt install ./pve-cli_amd64.deb

# .rpm
sudo rpm -i https://github.com/ciroiriarte/pve-cli/releases/latest/download/pve-cli_amd64.rpm
```

(Exact asset names are shown on the release page; arm64 builds are published too.)

## From source

Any platform with Go ≥ 1.22:

```bash
git clone https://github.com/ciroiriarte/pve-cli && cd pve-cli
make build           # ./pc
sudo install -m0755 pc /usr/local/bin/pc
# optional: man pages + completions
make docs
sudo cp -r dist/man/man1/*.1 /usr/local/share/man/man1/
```

`go install github.com/ciroiriarte/pve-cli/cmd/pc@latest` also works (installs
`pc` into `$GOBIN`).
