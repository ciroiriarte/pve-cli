package cli

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func newAPICmd(a *app) *cobra.Command {
	var data []string
	cmd := &cobra.Command{
		Use:   "api <METHOD> <path>",
		Short: "Make a raw authenticated API call (escape hatch)",
		Long: "Issue an arbitrary call against the Proxmox API, handling auth, base URL,\n" +
			"and TLS for you. Use this for endpoints the curated commands don't cover yet.",
		Example: "  pc api GET /cluster/resources\n" +
			"  pc api GET /nodes/pve-01/qemu\n" +
			"  pc api POST /nodes/pve-01/qemu/100/status/start\n" +
			"  pc api POST /nodes/pve-01/qemu/100/config --data cores=4 --data memory=4096",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			method := strings.ToUpper(args[0])
			params := url.Values{}
			for _, kv := range data {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					return fmt.Errorf("invalid --data %q: expected key=value", kv)
				}
				params.Add(k, v)
			}
			body, err := p.Raw(cmd.Context(), method, args[1], params)
			if err != nil {
				return err
			}
			os.Stdout.Write(body)
			if len(body) > 0 && body[len(body)-1] != '\n' {
				fmt.Fprintln(os.Stdout)
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVarP(&data, "data", "d", nil, "request parameter key=value (repeatable)")
	return cmd
}
