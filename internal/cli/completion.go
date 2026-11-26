package cli

// Live-API shell completion (#23). Guest vmids/names, node names, PDM remote
// ids and storage ids are suggested from the configured backend. Completion
// runs on every <TAB>, so it must never hang, prompt or print:
//
//   - one fetch is bounded by a short timeout (PVE_CLI_COMPLETION_TIMEOUT,
//     default 1.5s), including config/keyring resolution;
//   - the provider never prompts (TLS is a pin or system trust; an untrusted
//     cert is just an error), and every error yields no suggestions;
//   - stderr diagnostics (insecure/plaintext warnings, PDM inventory warnings,
//     --debug) are muted for the duration so the shell's line stays clean;
//   - results are cached on disk per server+identity for a short TTL, so
//     repeated tabs are instant and different clusters never mix.
//
// Wiring is central: wireLiveCompletion walks the tree once and attaches
// completers by the argument names in each command's Use line (<vmid>, <node>,
// <storage>, and <id> under `pc remote`) and by flag name (--node,
// --target-node, --remote, --vmid, --storage). A new command gets completion
// by naming its arguments/flags the usual way.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ciroiriarte/pve-cli/internal/config"
	"github.com/ciroiriarte/pve-cli/internal/domain"
	"github.com/ciroiriarte/pve-cli/internal/protocol"
	"github.com/ciroiriarte/pve-cli/internal/provider"
	"github.com/ciroiriarte/pve-cli/internal/transport"
)

const (
	defaultCompletionTimeout = 1500 * time.Millisecond
	completionCacheTTL       = 45 * time.Second

	compGuests   = "guests"
	compNodes    = "nodes"
	compRemotes  = "remotes"
	compStorages = "storages"
)

type completer = func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective)

// compEntry is one cached completion candidate (guest, node, remote, storage).
type compEntry struct {
	ID     string `json:"id"`
	Name   string `json:"name,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Node   string `json:"node,omitempty"`
	Remote string `json:"remote,omitempty"`
	Status string `json:"status,omitempty"`
}

type compCacheEntry struct {
	At    time.Time   `json:"at"`
	Items []compEntry `json:"items"`
}

func completionTimeout() time.Duration {
	if d, err := time.ParseDuration(os.Getenv("PVE_CLI_COMPLETION_TIMEOUT")); err == nil && d > 0 {
		return d
	}
	return defaultCompletionTimeout
}

// completionCachePath names the cache file for the active backend identity
// (provider, server, auth identity). The secret is never resolved here — a
// placeholder short-circuits keyring/inline lookups — and never hashed in.
func (a *app) completionCachePath() string {
	ov := a.overrides()
	if ov.Secret == "" && ov.SecretRef == "" {
		ov.Secret = "-"
	}
	f, err := config.Load(config.DefaultPath())
	if err != nil {
		return ""
	}
	s, err := config.Resolve(f, ov)
	if err != nil || s.Server == "" {
		return ""
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	h := sha256.Sum256([]byte(strings.Join([]string{s.Provider, s.Server, s.AuthType, s.TokenID, s.User}, "\x00")))
	return filepath.Join(dir, "pve-cli", "completion-"+hex.EncodeToString(h[:8])+".json")
}

func readCompCache(path string) map[string]compCacheEntry {
	m := map[string]compCacheEntry{}
	if path == "" {
		return m
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return m
	}
	_ = json.Unmarshal(b, &m)
	return m
}

// writeCompCache stores one data set, atomically (temp file + rename), 0600.
func writeCompCache(path, set string, items []compEntry) {
	if path == "" {
		return
	}
	m := readCompCache(path)
	m[set] = compCacheEntry{At: time.Now(), Items: items}
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	dir := filepath.Dir(path)
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, ".completion-*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Chmod(tmp.Name(), 0o600) != nil || os.Rename(tmp.Name(), path) != nil {
		_ = os.Remove(tmp.Name())
	}
}

// completionData returns a data set from the fresh cache or the live API. Any
// failure (unreachable, TLS, auth, timeout) returns nil.
func (a *app) completionData(cmd *cobra.Command, set string) []compEntry {
	path := a.completionCachePath()
	if e, ok := readCompCache(path)[set]; ok && time.Since(e.At) < completionCacheTTL && time.Since(e.At) >= 0 {
		return e.Items
	}

	// Mute everything that could write to the terminal mid-completion.
	restoreTransport := transport.SetStderr(io.Discard)
	defer restoreTransport()
	if devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0); err == nil {
		old := os.Stderr
		os.Stderr = devnull
		defer func() { os.Stderr = old; devnull.Close() }()
	}
	a.debug = false

	timeout := completionTimeout()
	parent := cmd.Context()
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	type result struct {
		items []compEntry
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		items, err := fetchCompletion(ctx, a, set)
		ch <- result{items, err}
	}()
	// The fetch honors ctx; the extra timer only guards work that doesn't
	// (e.g. a keyring call stuck on D-Bus). Small grace so a ctx-bounded fetch
	// reports its own error first.
	timer := time.NewTimer(timeout + 250*time.Millisecond)
	defer timer.Stop()
	select {
	case r := <-ch:
		if r.err != nil {
			return nil
		}
		writeCompCache(path, set, r.items)
		return r.items
	case <-timer.C:
		return nil
	}
}

func fetchCompletion(ctx context.Context, a *app, set string) ([]compEntry, error) {
	p, err := a.Provider()
	if err != nil {
		return nil, err
	}
	out := []compEntry{}
	switch set {
	case compGuests:
		gs, err := p.ListGuests(ctx, provider.GuestFilter{})
		if err != nil {
			return nil, err
		}
		for _, g := range gs {
			out = append(out, compEntry{ID: strconv.Itoa(g.VMID), Name: g.Name, Kind: string(g.Kind), Node: g.Node, Remote: g.Remote, Status: g.Status})
		}
	case compNodes:
		ns, err := p.ListNodes(ctx)
		if err != nil {
			return nil, err
		}
		for _, n := range ns {
			out = append(out, compEntry{ID: n.Name, Remote: n.Remote, Status: n.Status})
		}
	case compRemotes:
		rs, err := p.ListRemotes(ctx)
		if errors.Is(err, provider.ErrUnsupported) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		for _, r := range rs {
			out = append(out, compEntry{ID: r.ID, Kind: r.Type})
		}
	case compStorages:
		if p.Name() != "pve" {
			return out, nil
		}
		body, err := p.Raw(ctx, "GET", "/storage", nil)
		if err != nil {
			return nil, err
		}
		var rows []struct {
			Storage string `json:"storage"`
			Type    string `json:"type"`
		}
		if err := protocol.DecodeData(body, &rows); err != nil {
			return nil, err
		}
		for _, r := range rows {
			out = append(out, compEntry{ID: r.Storage, Kind: r.Type})
		}
	}
	return out, nil
}

// flagValue reads a local flag's string value, "" when absent.
func flagValue(cmd *cobra.Command, name string) string {
	if f := cmd.Flags().Lookup(name); f != nil {
		return f.Value.String()
	}
	return ""
}

// completeGuests suggests vmids (described by name) and names (described by
// vmid) of the given kind ("" = both), narrowed by --node/--remote when set.
func completeGuests(a *app, kind domain.GuestKind) completer {
	return func(cmd *cobra.Command, _ []string, tc string) ([]string, cobra.ShellCompDirective) {
		// Comma-list flags (e.g. `tag add --vmid 100,10<TAB>`): complete the
		// last element and keep what precedes it.
		lead := ""
		if i := strings.LastIndex(tc, ","); i >= 0 {
			lead, tc = tc[:i+1], tc[i+1:]
		}
		node, remote := flagValue(cmd, "node"), flagValue(cmd, "remote")
		var ids []compEntry
		names := map[string]string{}
		for _, g := range a.completionData(cmd, compGuests) {
			if (kind != "" && g.Kind != string(kind)) || (node != "" && g.Node != node) ||
				(remote != "" && !strings.EqualFold(g.Remote, remote)) {
				continue
			}
			if strings.HasPrefix(g.ID, tc) {
				ids = append(ids, g)
			}
			if g.Name != "" && strings.HasPrefix(g.Name, tc) {
				if _, err := strconv.Atoi(g.Name); err != nil {
					if prev, dup := names[g.Name]; dup {
						names[g.Name] = prev + "," + g.ID
					} else {
						names[g.Name] = g.ID
					}
				}
			}
		}
		sort.Slice(ids, func(i, j int) bool {
			x, _ := strconv.Atoi(ids[i].ID)
			y, _ := strconv.Atoi(ids[j].ID)
			if x != y {
				return x < y
			}
			return ids[i].Remote < ids[j].Remote
		})
		var out []string
		seen := map[string]bool{}
		for _, g := range ids {
			if seen[g.ID] { // same vmid on several PDM remotes: one suggestion
				continue
			}
			seen[g.ID] = true
			desc := g.Name
			if g.Remote != "" {
				desc += " @" + g.Remote
			}
			out = append(out, lead+g.ID+"\t"+strings.TrimSpace(desc))
		}
		nameList := make([]string, 0, len(names))
		for n := range names {
			nameList = append(nameList, n)
		}
		sort.Strings(nameList)
		for _, n := range nameList {
			out = append(out, lead+n+"\tvmid "+names[n])
		}
		return out, cobra.ShellCompDirectiveNoFileComp
	}
}

// completeNodes suggests online node names (narrowed by --remote on PDM).
func completeNodes(a *app) completer {
	return func(cmd *cobra.Command, _ []string, tc string) ([]string, cobra.ShellCompDirective) {
		remote := flagValue(cmd, "remote")
		seen := map[string]bool{}
		var out []string
		for _, n := range a.completionData(cmd, compNodes) {
			if n.Status != "" && n.Status != "online" {
				continue
			}
			if remote != "" && !strings.EqualFold(n.Remote, remote) {
				continue
			}
			if strings.HasPrefix(n.ID, tc) && !seen[n.ID] {
				seen[n.ID] = true
				out = append(out, n.ID)
			}
		}
		sort.Strings(out)
		return out, cobra.ShellCompDirectiveNoFileComp
	}
}

// completeIDs suggests the ids of a simple data set (remotes, storages), with
// the type as the description.
func completeIDs(a *app, set string) completer {
	return func(cmd *cobra.Command, _ []string, tc string) ([]string, cobra.ShellCompDirective) {
		var out []string
		for _, e := range a.completionData(cmd, set) {
			if strings.HasPrefix(e.ID, tc) {
				s := e.ID
				if e.Kind != "" {
					s += "\t" + e.Kind
				}
				out = append(out, s)
			}
		}
		sort.Strings(out)
		return out, cobra.ShellCompDirectiveNoFileComp
	}
}

// topLevel returns the name of c's ancestor directly under the root ("" for
// the root itself).
func topLevel(c *cobra.Command) string {
	for ; c.HasParent(); c = c.Parent() {
		if !c.Parent().HasParent() {
			return c.Name()
		}
	}
	return ""
}

// guestKindFor maps a command to the guest kind its tree is typed to.
func guestKindFor(c *cobra.Command) domain.GuestKind {
	switch topLevel(c) {
	case "vm":
		return domain.KindVM
	case "ct":
		return domain.KindCT
	}
	return ""
}

// argCompleter picks the completer for a positional argument by its Use name.
func argCompleter(a *app, c *cobra.Command, arg string) completer {
	inRemote := topLevel(c) == "remote"
	switch arg {
	case "vmid":
		// `vm create <vmid>` / `ct create <vmid>` take a new id.
		if c.Name() == "create" && guestKindFor(c) != "" && c.Parent().Parent() == c.Root() {
			return nil
		}
		return completeGuests(a, guestKindFor(c))
	case "node":
		if inRemote { // a node of the named remote, not of the backend
			return nil
		}
		return completeNodes(a)
	case "storage":
		return completeIDs(a, compStorages)
	case "id":
		if inRemote && c.Name() != "add" {
			return completeIDs(a, compRemotes)
		}
	}
	return nil
}

// wireLiveCompletion attaches live completers across the command tree. It only
// fills gaps: a command's own ValidArgsFunction / flag completion wins.
func wireLiveCompletion(a *app, c *cobra.Command) {
	if c.ValidArgsFunction == nil {
		words := strings.Fields(c.Use)
		var comps []completer
		wired := false
		for _, w := range words[min(1, len(words)):] {
			if w == "--" {
				break
			}
			fn := argCompleter(a, c, strings.Trim(w, "<>[]."))
			wired = wired || fn != nil
			comps = append(comps, fn)
		}
		if wired {
			c.ValidArgsFunction = func(cmd *cobra.Command, args []string, tc string) ([]string, cobra.ShellCompDirective) {
				if len(args) < len(comps) && comps[len(args)] != nil {
					return comps[len(args)](cmd, args, tc)
				}
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
		}
	}

	flags := map[string]completer{
		"node":        completeNodes(a),
		"target-node": completeNodes(a),
		"remote":      completeIDs(a, compRemotes),
		"vmid":        completeGuests(a, guestKindFor(c)),
		"storage":     completeIDs(a, compStorages),
	}
	if topLevel(c) == "remote" {
		delete(flags, "node") // `remote add/update --node` is an endpoint address
	}
	for name, fn := range flags {
		if c.Flags().Lookup(name) != nil {
			if _, exists := c.GetFlagCompletionFunc(name); !exists {
				_ = c.RegisterFlagCompletionFunc(name, fn)
			}
		}
	}
	for _, sub := range c.Commands() {
		wireLiveCompletion(a, sub)
	}
}
