package cli

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/ciroiriarte/pve-cli/internal/domain"
	"github.com/ciroiriarte/pve-cli/internal/output"
	"github.com/ciroiriarte/pve-cli/internal/provider"
)

// newRemoteCmd manages PDM remotes. It is always registered but refuses to run
// against a provider that does not support remotes (i.e. direct PVE).
func newRemoteCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remote",
		Short: "Manage PDM remotes (clusters); requires the pdm provider",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use: "list", Short: "List clusters managed by PDM", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				p, err := a.providerWithRemotes()
				if err != nil {
					return err
				}
				remotes, err := p.ListRemotes(cmd.Context())
				if err != nil {
					return err
				}
				sort.Slice(remotes, func(i, j int) bool { return remotes[i].ID < remotes[j].ID })
				return a.render(remotesTable(remotes))
			},
		},
		&cobra.Command{
			Use: "show <id>", Short: "Show a remote", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				p, err := a.providerWithRemotes()
				if err != nil {
					return err
				}
				remotes, err := p.ListRemotes(cmd.Context())
				if err != nil {
					return err
				}
				for _, r := range remotes {
					if r.ID == args[0] {
						return a.render(remotesTable([]domain.Remote{r}))
					}
				}
				return fmt.Errorf("remote %q not found", args[0])
			},
		},
	)
	return cmd
}

// providerWithRemotes builds the provider and verifies it supports remotes.
func (a *app) providerWithRemotes() (provider.Provider, error) {
	p, err := a.Provider()
	if err != nil {
		return nil, err
	}
	if !p.Capabilities().Remotes {
		return nil, fmt.Errorf("remotes are only available with the PDM provider (current provider: %s); set provider: pdm in your profile", p.Name())
	}
	return p, nil
}

func remotesTable(remotes []domain.Remote) output.Tabular {
	t := output.Tabular{Columns: []string{"id", "type", "nodes", "web-url"}, Raw: remotes}
	for _, r := range remotes {
		t.Rows = append(t.Rows, []string{r.ID, r.Type, r.Nodes, r.Web})
	}
	return t
}
