# pve-cli cookbook

Practical, copy-pasteable recipes for day-to-day work with `pc`. For install and
config see [install.md](install.md) and [configuration.md](configuration.md);
for the PVE-vs-PDM capability split see [providers.md](providers.md); for the
exact endpoint coverage see [coverage.md](coverage.md).

> Conventions: `json` is the stable scripting contract (table layout is not).
> `-o value` strips headers/borders for `awk`/`xargs`. Exit codes are
> deterministic (`0` ok · `2` auth/config · `3` not-found · `4` API/5xx ·
> `5` task-failed · `6` validation) — script against them.

## Contexts & providers

```bash
pc context list                       # configured contexts
pc context use corp                   # switch default
pc --context homelab node list        # one-off override
pc --provider pdm remote list         # talk to PDM instead of a cluster
```

## Find things fast

```bash
pc vm list                            # VMs (node auto-resolved)
pc vm list --status running           # filter
pc guest list                         # VMs + containers, unified
pc vm list -c id -c name -c status -o value | sort -k3   # script-friendly columns
pc vm show 100                        # full snapshot: config + live status
pc vm config 100                      # raw config only (--set to modify)
pc vm status 100                      # live runtime status only
pc node/guest list -o json | jq '.[]|select(.status=="running")|.vmid'

# Don't know (or care) whether an id is a VM or a container? Use `pc guest`:
# it resolves the type from the vmid and routes accordingly.
pc guest show 102 ; pc guest status 102      # works for a VM or a CT
pc guest start 102 ; pc guest stop 102 --yes # same verbs, type auto-detected
pc guest console 100                          # serial console (PVE, ticket auth)
```

## Lifecycle (every mutation supports `--wait`/`--no-wait`/`--wait-timeout`)

```bash
pc vm start 100 --wait                # spinner → ✔ (TTY); UPID immediately otherwise
pc vm shutdown 100 --wait             # graceful (ACPI); `stop` is hard
pc vm reboot 100 ; pc vm suspend 100 ; pc vm resume 100
pc vm reset 100 --yes                 # hard reset
pc vm migrate 100 --target-node pve-02 --online
pc vm clone 100 200 --name web-02 --full --storage local-zfs --wait
pc vm delete 200 --yes --purge --wait
```

## Disks, cloud-init, console

```bash
pc vm resize 100 --disk scsi0 --size +10G
pc vm move-disk 100 --disk scsi0 --storage local-zfs --delete
pc vm unlink 100 --disks unused0
pc vm config 100 --set memory=4096 --set cores=4
pc vm cloudinit 100                   # pending cloud-init changes
pc vm cloudinit 100 --dump user       # rendered user-data
pc vm cloudinit 100 --regenerate
pc vm console 100                     # serial console (ticket auth; Ctrl-] to detach)
pc vm template 100 --yes              # convert to a template
```

## Guest agent

```bash
pc vm agent ping 100                  # liveness (exit 0 = healthy)
pc vm agent network 100               # interfaces/IPs
pc vm agent osinfo 100                # distro / version / kernel
pc vm agent exec 100 -- uname -a      # run a command in the guest
pc vm agent fstrim 100 ; pc vm agent set-password 100 --user root --password ...
```

## Snapshots & backups

```bash
pc vm snapshot create 100 pre-upgrade
pc vm snapshot list 100
pc vm snapshot rollback 100 pre-upgrade --yes
pc vm snapshot delete 100 pre-upgrade --yes
pc backup create 100 --storage backup-nfs --mode snapshot --wait
pc backup list --storage backup-nfs                  # --node optional (shared storage)
pc backup job list                                   # scheduled vzdump jobs
pc backup job create --storage backup-nfs --schedule '02:00' --all   # first-class flags
pc backup job create --storage backup-nfs --vmid 100,101 --mode snapshot
#   (--set k=v still works as an escape hatch for any other field)
```

## Storage, pools, HA

```bash
pc storage list ; pc storage status local           # --node optional (defaults to an online node)
pc storage content list local --node pve-01 --type iso
pc storage content upload local /isos/debian-13.iso        # ISO/template upload (content auto-detected)
pc storage content upload local tmpl.tar.zst --content vztmpl --checksum <sha256> --checksum-algorithm sha256
pc storage content delete local backup/vzdump-qemu-100-....vma.zst --node pve-01
pc storage prune-backups backup-nfs --node pve-01            # dry-run; add --apply
pc pool list ; pc pool create web ; pc pool update web --vms 100,101
pc ha status ; pc ha resource list ; pc ha resource add vm:100 --group dc1 --state started
```

## Networking: firewall & SDN

```bash
# firewall — scope with --node / --vmid (default: cluster)
pc firewall rules ; pc firewall rules --node pve-01 ; pc firewall rules --vmid 100
pc firewall rule add --node pve-01 --set type=in --set action=ACCEPT --set proto=tcp --set dport=22
pc firewall options --vmid 100 ; pc firewall macros
# SDN (PVE /cluster/sdn) — remember to apply
pc sdn zone list ; pc sdn vnet list ; pc sdn subnet list vnet0
pc sdn zone create dmz --type vlan --bridge vmbr0
pc sdn vnet create v100 --zone dmz --tag 100
pc sdn subnet create v100 10.0.0.0/24 --gateway 10.0.0.1 --snat
pc sdn apply                                          # commit pending SDN config
```

## Ceph (PVE, node-scoped)

```bash
pc ceph health                       # cluster-wide reads: --node optional (auto-picks a node)
pc ceph osd list ; pc ceph osd out 12 --node pve-01   # writes need --node + confirm (--yes to skip)
pc ceph pool list
pc ceph service restart --node pve-01 --service mon.pve-01
```

## Access (users, tokens, roles, ACLs)

```bash
pc access user list ; pc access user create svc@pve --set comment=automation
pc access token create svc@pve cli   # prints the secret ONCE
pc access acl set --path /vms/100 --roles PVEVMAdmin --ids svc@pve
pc access role list ; pc access realm list
```

## PDM (fleet across clusters)

```bash
pc --provider pdm remote list                         # managed clusters
pc --provider pdm guest list                          # guests across all remotes (remote column)
pc --provider pdm vm start 100 --remote MP02          # --remote disambiguates a shared vmid
pc --provider pdm vm snapshot create 100 pre --remote MP02
pc --provider pdm remote cluster status MP02 ; pc --provider pdm remote updates
pc --provider pdm ceph clusters                       # cross-cluster Ceph health
```

**What PDM does NOT proxy** (use `provider: pve` against the cluster directly):
provisioning (`create`/`clone`/`config --set`/`delete`), the **guest agent**
(`agent`, so no live OS/IP detail), `cloudinit`, disk ops (`resize`/`move-disk`/
`unlink`), `console`, `sendkey`, `template`, `reset`. The tool refuses these
cleanly on PDM. snapshot/migrate/power/status/pending/rrddata/firewall work.

## Escape hatch (anything not curated)

```bash
pc raw                                # discover the live API tree
pc raw nodes pve-01 qemu 100 status current --method GET
pc api GET /cluster/resources -o json | jq '.[].type' | sort -u
pc --provider pdm api GET /resources/status
```

> A mutating method (`POST`/`PUT`/`DELETE`) on `raw`/`api` prompts for
> confirmation first — pass `--yes` to skip it in scripts.

## Reports (compose `vm list` + per-guest probes)

### Guest-agent health
```bash
pc vm list -o json \
  | jq -r '.[]|select(.status=="running")|.vmid' \
  | while read v; do
      pc vm agent ping "$v" >/dev/null 2>&1 && s=healthy || s=NO-AGENT
      echo "$v $s"
    done
```

### OS type & version inventory
`ostype` (config) is a coarse hint (`l26`, `win11`); the **guest agent** gives the
real distro/version/kernel — so the report uses the agent when available and
falls back to `ostype`:
```bash
pc vm list -o json | jq -r '.[]|"\(.vmid)\t\(.status)"' | while IFS=$'\t' read -r v st; do
  ostype=$(pc vm show "$v" -o json | jq -r '.[]|select(.key=="ostype").value // "-"')
  os="-"
  [ "$st" = running ] && os=$(pc vm agent osinfo "$v" -o json 2>/dev/null \
        | jq -r '.result["pretty-name"] // "(no agent)"')
  printf "%-6s %-9s %-8s %s\n" "$v" "$st" "$ostype" "$os"
done
```
Fleet-wide via PDM gives inventory + `ostype` only; for real OS **version** you
must reach each cluster's PVE API directly (the agent isn't proxied — see above).
