#
# RPM spec for pve-cli (binary: pc) — openSUSE Leap/Slowroll and Rocky Linux.
# Built on build.opensuse.org (project home:ciriarte:pve-cli) from the tarball +
# vendored Go modules produced by the _service file. Network-isolated build:
# modules come from the vendor/ tree, never the network.
#
Name:           pve-cli
Version:        0
Release:        0
Summary:        Unofficial remote CLI for Proxmox VE (command: pc)
License:        Apache-2.0
Group:          System/Management
URL:            https://github.com/ciroiriarte/pve-cli
Source0:        %{name}-%{version}.tar.gz
Source1:        vendor.tar.gz
BuildRequires:  go >= 1.22
%if 0%{?suse_version}
BuildRequires:  golang-packaging
%endif

%description
pve-cli is a remote-first, OpenStack-Client-inspired command-line client for
Proxmox VE (and, via Proxmox Datacenter Manager, fleets of clusters). It talks
to the published REST API, so nothing needs to be installed on the nodes. The
installed command is "pc".

This is an unofficial, community tool and is not affiliated with or endorsed by
Proxmox Server Solutions GmbH.

%prep
%autosetup -n %{name}-%{version}
# Unpack vendored Go modules for an offline build.
tar -xf %{SOURCE1}

%build
export GOFLAGS="-mod=vendor -trimpath"
export CGO_ENABLED=0
go build -ldflags "-s -w \
  -X %{name}/internal/version.Version=%{version} \
  -X github.com/ciroiriarte/pve-cli/internal/version.Version=%{version}" \
  -o pc ./cmd/pc
# Generate man pages, completions, and the markdown reference (offline).
go run ./cmd/gendocs dist

%install
install -Dm0755 pc %{buildroot}%{_bindir}/pc
# Man pages
for m in dist/man/man1/*.1; do
  install -Dm0644 "$m" "%{buildroot}%{_mandir}/man1/$(basename "$m")"
done
# Shell completions
install -Dm0644 dist/completions/pc      %{buildroot}%{_datadir}/bash-completion/completions/pc
install -Dm0644 dist/completions/_pc     %{buildroot}%{_datadir}/zsh/site-functions/_pc
install -Dm0644 dist/completions/pc.fish %{buildroot}%{_datadir}/fish/vendor_completions.d/pc.fish

%check
export GOFLAGS="-mod=vendor"
go test ./...

%files
%license LICENSE
%doc README.md NOTICE
%{_bindir}/pc
%{_mandir}/man1/pc*.1*
%{_datadir}/bash-completion/completions/pc
%dir %{_datadir}/zsh/site-functions
%{_datadir}/zsh/site-functions/_pc
%{_datadir}/fish/vendor_completions.d/pc.fish

%changelog
