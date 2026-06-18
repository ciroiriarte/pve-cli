package cli

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	pdmschema "github.com/ciroiriarte/pve-cli/internal/generated/pdm"
	pveschema "github.com/ciroiriarte/pve-cli/internal/generated/pve"
	"github.com/ciroiriarte/pve-cli/internal/protocol"
	"github.com/ciroiriarte/pve-cli/internal/schema"
)

// activeSchema returns the embedded schema for the configured backend.
func (a *app) activeSchema() (*schema.API, error) {
	if a.providerName() == "pdm" {
		return pdmschema.Schema()
	}
	return pveschema.Schema()
}

func newRawCmd(a *app) *cobra.Command {
	var method string
	var data []string

	cmd := &cobra.Command{
		Use:   "raw [segments...]",
		Short: "Call any API endpoint by walking the schema (full coverage)",
		Long: "raw exposes the entire Proxmox API by walking the embedded schema tree.\n" +
			"Type path segments (real values for {params}); --help on a path shows its\n" +
			"methods and parameters. This guarantees coverage of endpoints the curated\n" +
			"commands don't wrap yet.",
		Example: "  pc raw                                   # list top-level segments\n" +
			"  pc raw version                           # GET /version\n" +
			"  pc raw nodes pve-01 qemu 100 status current\n" +
			"  pc raw nodes pve-01 qemu 100 config --method POST -d cores=4\n" +
			"  pc raw nodes pve-01 qemu --help          # describe the endpoint",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			api, err := a.activeSchema()
			if err != nil {
				return err
			}
			node, path, err := api.Resolve(args)
			if err != nil {
				return err
			}
			if node == nil { // no segments: list roots
				fmt.Fprintln(os.Stdout, "Top-level segments:")
				for _, r := range api.Roots() {
					fmt.Fprintln(os.Stdout, "  "+r)
				}
				return nil
			}

			m := strings.ToUpper(method)
			ep := node.Methods[m]
			if ep == nil {
				// Not callable with this method: guide the user.
				if len(node.Methods) == 0 {
					if labels := schema.ChildLabels(node); len(labels) > 0 {
						fmt.Fprintf(os.Stdout, "%s is not an endpoint. Sub-segments:\n", path)
						for _, l := range labels {
							fmt.Fprintln(os.Stdout, "  "+l)
						}
						return nil
					}
					return fmt.Errorf("%s has no callable methods", path)
				}
				return fmt.Errorf("method %s not available on %s; available: %s",
					m, path, strings.Join(node.MethodNames(), ", "))
			}

			p, err := a.Provider()
			if err != nil {
				return err
			}
			params, err := parseDataParams(data)
			if err != nil {
				return err
			}
			body, err := p.Raw(cmd.Context(), m, path, params)
			if err != nil {
				return err
			}
			return printRawData(body)
		},
	}
	cmd.Flags().StringVarP(&method, "method", "X", "GET", "HTTP method")
	cmd.Flags().StringArrayVarP(&data, "data", "d", nil, "request parameter key=value (repeatable)")

	// Override help so `pc raw <path> --help` describes the schema endpoint.
	cmd.SetHelpFunc(func(c *cobra.Command, _ []string) {
		segs := c.Flags().Args()
		if len(segs) > 0 {
			if api, err := a.activeSchema(); err == nil {
				if node, path, err := api.Resolve(segs); err == nil && node != nil {
					describeNode(os.Stdout, node, path)
					return
				}
			}
		}
		_ = c.Usage()
	})
	return cmd
}

func parseDataParams(data []string) (url.Values, error) {
	params := url.Values{}
	for _, kv := range data {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return nil, fmt.Errorf("invalid -d %q: expected key=value", kv)
		}
		params.Add(k, v)
	}
	return params, nil
}

// printRawData unwraps the Proxmox envelope and prints the data as JSON.
func printRawData(body []byte) error {
	var data any
	if err := protocol.DecodeData(body, &data); err != nil {
		// Fall back to printing the raw body if it isn't a standard envelope.
		os.Stdout.Write(body)
		if len(body) > 0 && body[len(body)-1] != '\n' {
			fmt.Fprintln(os.Stdout)
		}
		return nil
	}
	return printJSON(os.Stdout, data)
}

func describeNode(w io.Writer, node *schema.Node, path string) {
	fmt.Fprintf(w, "Path: %s\n", path)
	if labels := schema.ChildLabels(node); len(labels) > 0 {
		fmt.Fprintf(w, "Sub-segments: %s\n", strings.Join(labels, ", "))
	}
	if len(node.Methods) == 0 {
		fmt.Fprintln(w, "(no callable methods at this path)")
		return
	}
	for _, m := range node.MethodNames() {
		ep := node.Methods[m]
		fmt.Fprintf(w, "\n%s %s\n", m, path)
		if ep.Description != "" {
			fmt.Fprintf(w, "  %s\n", ep.Description)
		}
		for _, prm := range ep.Parameters {
			req := "optional"
			if !prm.Optional {
				req = "required"
			}
			line := fmt.Sprintf("  --%s (%s, %s)", prm.Name, prm.Type, req)
			if len(prm.Enum) > 0 {
				line += " [" + strings.Join(prm.Enum, "|") + "]"
			}
			fmt.Fprintln(w, line)
			if prm.Description != "" {
				fmt.Fprintf(w, "      %s\n", firstLine(prm.Description))
			}
		}
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
