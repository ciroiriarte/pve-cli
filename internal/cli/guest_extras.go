package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ciroiriarte/pve-cli/internal/domain"
	"github.com/ciroiriarte/pve-cli/internal/provider"
)

// guestBaseFn resolves the provider, guest, and API base path for a vmid arg.
type guestBaseFn = func(*cobra.Command, string) (provider.Provider, domain.Guest, string, error)

// newGuestExtraCmds returns additional curated guest commands beyond the core
// lifecycle: disk/template ops for both kinds, plus qemu-only guest-agent,
// cloud-init, and console-input helpers. All are provider-aware via guestBase.
func newGuestExtraCmds(a *app, spec guestSpec) []*cobra.Command {
	var node, remote string
	scope := func(c *cobra.Command) {
		c.Flags().StringVar(&node, "node", "", "node hosting the guest (skips auto-resolution)")
		c.Flags().StringVar(&remote, "remote", "", "PDM remote that hosts the guest")
	}
	var base guestBaseFn = func(cmd *cobra.Command, idArg string) (provider.Provider, domain.Guest, string, error) {
		// These extras (agent, cloud-init, resize, move-disk/volume, unlink,
		// sendkey, template, reset) are NOT exposed by PDM's proxy — it only
		// proxies status/config/snapshot/migrate/pending/rrddata/firewall/power.
		// Refuse cleanly on PDM instead of surfacing a raw 404.
		if p, err := a.Provider(); err != nil {
			return nil, domain.Guest{}, "", err
		} else if p.Name() == "pdm" {
			return nil, domain.Guest{}, "", fmt.Errorf("this operation is not available via PDM (its proxy does not expose guest-agent / disk-management endpoints); set provider: pve and target the cluster directly")
		}
		return resolveGuestBase(cmd, a, spec, idArg, node, remote)
	}

	cmds := []*cobra.Command{}

	// resize a disk/volume (both kinds)
	var disk, size string
	resize := &cobra.Command{
		Use: "resize <vmid>", Short: fmt.Sprintf("Grow a %s disk/volume", spec.label),
		Example: fmt.Sprintf("  pc %s resize 100 --disk scsi0 --size +10G", spec.noun), Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, g, b, err := base(cmd, args[0])
			if err != nil {
				return err
			}
			if disk == "" || size == "" {
				return fmt.Errorf("--disk and --size are required (e.g. --disk scsi0 --size +10G)")
			}
			return rawMutate(cmd.Context(), a, p, "PUT", b+"/resize",
				map[string][]string{"disk": {disk}, "size": {size}},
				fmt.Sprintf("resize %s %d %s", spec.label, g.VMID, disk), true, 0)
		},
	}
	scope(resize)
	resize.Flags().StringVar(&disk, "disk", "", "disk/volume key (e.g. scsi0, rootfs)")
	resize.Flags().StringVar(&size, "size", "", "new size or increment (e.g. +10G, 64G)")
	cmds = append(cmds, resize)

	// convert to template (both kinds)
	tmpl := &cobra.Command{
		Use: "template <vmid>", Short: fmt.Sprintf("Convert a %s into a template", spec.label), Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, g, b, err := base(cmd, args[0])
			if err != nil {
				return err
			}
			if err := confirm(a, fmt.Sprintf("convert %s %d into a template (irreversible)?", spec.label, g.VMID)); err != nil {
				return err
			}
			return rawMutate(cmd.Context(), a, p, "POST", b+"/template", nil,
				fmt.Sprintf("templatize %s %d", spec.label, g.VMID), true, 0)
		},
	}
	scope(tmpl)
	cmds = append(cmds, tmpl)

	if spec.kind == "qemu" {
		// reset (hard) — qemu only
		reset := &cobra.Command{
			Use: "reset <vmid>", Short: "Hard-reset a VM", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				p, g, b, err := base(cmd, args[0])
				if err != nil {
					return err
				}
				if err := confirm(a, fmt.Sprintf("hard-reset VM %d?", g.VMID)); err != nil {
					return err
				}
				return rawMutate(cmd.Context(), a, p, "POST", b+"/status/reset", nil, fmt.Sprintf("reset VM %d", g.VMID), true, 0)
			},
		}
		scope(reset)

		// move a disk to another storage
		var mdDisk, mdStorage, mdFormat string
		var mdDelete bool
		move := &cobra.Command{
			Use: "move-disk <vmid>", Short: "Move a VM disk to another storage", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				p, _, b, err := base(cmd, args[0])
				if err != nil {
					return err
				}
				if mdDisk == "" || mdStorage == "" {
					return fmt.Errorf("--disk and --storage are required")
				}
				params := map[string][]string{"disk": {mdDisk}, "storage": {mdStorage}}
				if mdFormat != "" {
					params["format"] = []string{mdFormat}
				}
				if mdDelete {
					params["delete"] = []string{"1"}
				}
				return rawMutate(cmd.Context(), a, p, "POST", b+"/move_disk", params,
					fmt.Sprintf("move %s disk %s -> %s", spec.label, mdDisk, mdStorage), true, 0)
			},
		}
		scope(move)
		move.Flags().StringVar(&mdDisk, "disk", "", "disk key (e.g. scsi0)")
		move.Flags().StringVar(&mdStorage, "storage", "", "target storage")
		move.Flags().StringVar(&mdFormat, "format", "", "target format (raw|qcow2|vmdk)")
		move.Flags().BoolVar(&mdDelete, "delete", false, "delete the source after a successful move")

		// unlink (detach) disks
		var ulDisks string
		var ulForce bool
		unlink := &cobra.Command{
			Use: "unlink <vmid>", Short: "Detach (unlink) one or more VM disks", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				p, _, b, err := base(cmd, args[0])
				if err != nil {
					return err
				}
				if ulDisks == "" {
					return fmt.Errorf("--disks is required (comma-separated, e.g. unused0,scsi1)")
				}
				params := map[string][]string{"idlist": {ulDisks}}
				if ulForce {
					params["force"] = []string{"1"}
				}
				return rawMutate(cmd.Context(), a, p, "PUT", b+"/unlink", params,
					fmt.Sprintf("unlink %s disks %s", spec.label, ulDisks), true, 0)
			},
		}
		scope(unlink)
		unlink.Flags().StringVar(&ulDisks, "disks", "", "comma-separated disk keys")
		unlink.Flags().BoolVar(&ulForce, "force", false, "also remove the disk image")

		// sendkey
		sendkey := &cobra.Command{
			Use: "sendkey <vmid> <key>", Short: "Send a key event to a VM (e.g. ctrl-alt-delete)", Args: cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				p, _, b, err := base(cmd, args[0])
				if err != nil {
					return err
				}
				return rawMutate(cmd.Context(), a, p, "PUT", b+"/sendkey", map[string][]string{"key": {args[1]}},
					"sendkey "+args[1], true, 0)
			},
		}
		scope(sendkey)

		cmds = append(cmds, reset, move, unlink, sendkey, newGuestCloudInitCmd(a, spec, base, scope), newGuestAgentCmd(a, spec, base, scope))
	} else { // lxc
		var mvVolume, mvStorage string
		var mvDelete bool
		mv := &cobra.Command{
			Use: "move-volume <vmid>", Short: "Move a container volume to another storage", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				p, g, b, err := base(cmd, args[0])
				if err != nil {
					return err
				}
				if mvVolume == "" || mvStorage == "" {
					return fmt.Errorf("--volume and --storage are required")
				}
				params := map[string][]string{"volume": {mvVolume}, "storage": {mvStorage}}
				if mvDelete {
					params["delete"] = []string{"1"}
				}
				return rawMutate(cmd.Context(), a, p, "POST", b+"/move_volume", params,
					fmt.Sprintf("move ct %d volume %s -> %s", g.VMID, mvVolume, mvStorage), true, 0)
			},
		}
		scope(mv)
		mv.Flags().StringVar(&mvVolume, "volume", "", "volume key (e.g. rootfs, mp0)")
		mv.Flags().StringVar(&mvStorage, "storage", "", "target storage")
		mv.Flags().BoolVar(&mvDelete, "delete", false, "delete the source after a successful move")
		cmds = append(cmds, mv)
	}

	return cmds
}

// newGuestCloudInitCmd manages the cloud-init drive of a VM.
func newGuestCloudInitCmd(a *app, spec guestSpec, base guestBaseFn, scope func(*cobra.Command)) *cobra.Command {
	var dumpType string
	var regenerate bool
	cmd := &cobra.Command{
		Use: "cloudinit <vmid>", Short: "Show pending cloud-init changes, dump, or regenerate the drive", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, g, b, err := base(cmd, args[0])
			if err != nil {
				return err
			}
			switch {
			case regenerate:
				return rawMutate(cmd.Context(), a, p, "PUT", b+"/cloudinit", nil, fmt.Sprintf("regenerate cloud-init for VM %d", g.VMID), true, 0)
			case dumpType != "":
				return a.renderGet(cmd, p, b+"/cloudinit/dump?type="+dumpType)
			default:
				return a.renderGet(cmd, p, b+"/cloudinit", "key", "old", "new")
			}
		},
	}
	scope(cmd)
	cmd.Flags().StringVar(&dumpType, "dump", "", "dump generated config: user|network|meta")
	cmd.Flags().BoolVar(&regenerate, "regenerate", false, "regenerate the cloud-init drive")
	return cmd
}

// newGuestAgentCmd exposes common QEMU guest-agent operations.
func newGuestAgentCmd(a *app, spec guestSpec, base guestBaseFn, scope func(*cobra.Command)) *cobra.Command {
	cmd := &cobra.Command{Use: "agent", Short: "Interact with the QEMU guest agent"}
	read := func(use, short, suffix string, cols ...string) *cobra.Command {
		c := &cobra.Command{
			Use: use, Short: short, Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				p, _, b, err := base(cmd, args[0])
				if err != nil {
					return err
				}
				return a.renderGet(cmd, p, b+"/agent/"+suffix, cols...)
			},
		}
		scope(c)
		return c
	}
	post := func(use, short, suffix string) *cobra.Command {
		c := &cobra.Command{
			Use: use, Short: short, Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				p, _, b, err := base(cmd, args[0])
				if err != nil {
					return err
				}
				return rawMutate(cmd.Context(), a, p, "POST", b+"/agent/"+suffix, nil, suffix, true, 0)
			},
		}
		scope(c)
		return c
	}
	cmd.AddCommand(
		read("network <vmid>", "Guest network interfaces", "network-get-interfaces", "name", "ip-addresses"),
		read("osinfo <vmid>", "Guest OS info", "get-osinfo"),
		read("users <vmid>", "Logged-in guest users", "get-users", "user", "login-time"),
		read("info <vmid>", "Guest agent info", "info"),
		post("ping <vmid>", "Ping the guest agent", "ping"),
		post("fstrim <vmid>", "Trim guest filesystems", "fstrim"),
		post("shutdown <vmid>", "Shut down via the guest agent", "shutdown"),
		newAgentExecCmd(a, base, scope),
		newAgentSetPasswordCmd(a, base, scope),
	)
	return cmd
}

func newAgentExecCmd(a *app, base guestBaseFn, scope func(*cobra.Command)) *cobra.Command {
	cmd := &cobra.Command{
		Use: "exec <vmid> -- <command> [args...]", Short: "Run a command in the guest via the agent",
		Example: "  pc vm agent exec 100 -- uname -a", Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, _, b, err := base(cmd, args[0])
			if err != nil {
				return err
			}
			params := map[string][]string{"command": {strings.Join(args[1:], " ")}}
			body, err := p.Raw(cmd.Context(), "POST", b+"/agent/exec", params)
			if err != nil {
				return err
			}
			fmt.Fprintln(stderrWriter(), string(body))
			return nil
		},
	}
	scope(cmd)
	return cmd
}

func newAgentSetPasswordCmd(a *app, base guestBaseFn, scope func(*cobra.Command)) *cobra.Command {
	var user, password string
	cmd := &cobra.Command{
		Use: "set-password <vmid>", Short: "Set a guest user's password via the agent", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, _, b, err := base(cmd, args[0])
			if err != nil {
				return err
			}
			if user == "" || password == "" {
				return fmt.Errorf("--user and --password are required")
			}
			return rawMutate(cmd.Context(), a, p, "POST", b+"/agent/set-user-password",
				map[string][]string{"username": {user}, "password": {password}}, "set guest password", true, 0)
		},
	}
	scope(cmd)
	cmd.Flags().StringVar(&user, "user", "", "guest username")
	cmd.Flags().StringVar(&password, "password", "", "new password")
	return cmd
}
