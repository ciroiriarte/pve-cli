package cli

// Live-API shell completion (#23). Guest vmids/names, node names, PDM remote
// ids, storage ids and tag names are suggested from the configured backend.
// Completion runs on every <TAB>, so it must never hang, prompt or print:
//
//   - everything that can block — config load, settings/keyring resolution,
//     cache I/O and the API call — runs in one worker bounded by a short
//     timeout (PVE_CLI_COMPLETION_TIMEOUT, default 1.5s);
//   - the provider never prompts (TLS is a pin or system trust; an untrusted
//     cert is just an error), and every error yields no suggestions;
//   - an insecure backend (verification off, no pin) is never contacted:
//     completion can't show the insecure-credentials warning (§4), so it
//     doesn't send credentials;
//   - nothing global is redirected: the provider is built locally with debug
//     off, and config/PDM warnings go to io.Discard via per-call sinks;
//   - results are cached on disk per backend identity for a short TTL, so
//     repeated tabs are instant and different clusters never mix. Only cache
//     files we own (regular, 0600, in a 0700 dir we own) are trusted.
//
// Wiring is central: wireLiveCompletion walks the tree once and attaches
// completers by the argument names in each command's Use line (<vmid>, <node>,
// <storage>, and <id> under `pc remote`) and by flag name (--node,
// --target-node, --remote, --vmid, --storage), local or persistent. A new
// command gets completion by naming its arguments/flags the usual way.
// Positional slots it doesn't own keep cobra's default (file) completion.

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
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"

	"github.com/ciroiriarte/pve-cli/internal/config"
	"github.com/ciroiriarte/pve-cli/internal/domain"
	"github.com/ciroiriarte/pve-cli/internal/protocol"
	"github.com/ciroiriarte/pve-cli/internal/provider"
)

const (
	defaultCompletionTimeout = 1500 * time.Millisecond
	completionCacheTTL       = 45 * time.Second
	maxCompCacheBytes        = 8 << 20

	compGuests   = "guests"
	compNodes    = "nodes"
	compRemotes  = "remotes"
	compStorages = "storages"
	compTags     = "tags"
)

type completer = func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective)

// compEntry is one cached completion candidate (guest, node, remote, storage, tag).
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

// compResult is a data set plus the context's pinned remote (PDM), which
// scopes guest suggestions the way resolveGuest scopes execution (§13).
type compResult struct {
	items         []compEntry
	defaultRemote string
}

// completionResolve resolves settings; a seam so tests can simulate a secret
// lookup (keyring) that blocks.
var completionResolve = config.Resolve

func completionTimeout() time.Duration {
	if d, err := time.ParseDuration(os.Getenv("PVE_CLI_COMPLETION_TIMEOUT")); err == nil && d > 0 {
		return d
	}
	return defaultCompletionTimeout
}

// cacheRootOverride, when holding a non-empty string, replaces
// os.UserCacheDir as the cache root. Test-only seam: os.UserCacheDir ignores
// XDG_CACHE_HOME on macOS/Windows, so tests set this to stay off the real
// user cache on every OS. Atomic so abandoned completion workers never race
// a test swapping it.
var cacheRootOverride atomic.Value

func completionCacheRoot() (string, error) {
	if s, _ := cacheRootOverride.Load().(string); s != "" {
		return s, nil
	}
	return os.UserCacheDir()
}

// completionCachePath names the cache file for a backend identity: provider,
// server, auth identity and TLS identity (pin/CA, so two tunnels behind the
// same URL don't mix). Never the secret.
func completionCachePath(s *config.Settings) string {
	dir, err := completionCacheRoot()
	if err != nil {
		return ""
	}
	h := sha256.Sum256([]byte(strings.Join([]string{s.Provider, s.Server, s.AuthType, s.TokenID, s.User, s.TLSFinger, s.TLSCAFile}, "\x00")))
	return filepath.Join(dir, "pve-cli", "completion-"+hex.EncodeToString(h[:8])+".json")
}

// readCompCache loads the cache file, ignoring anything we don't trust: a
// directory that isn't ours with mode 0700 (in a shared writable dir another
// user could rename one of our valid files over another key), a symlink, a
// non-regular file, or one not owned by us with mode 0600. Reading never
// changes permissions.
func readCompCache(path string) map[string]compCacheEntry {
	m := map[string]compCacheEntry{}
	if path == "" || !cacheDirSafe(filepath.Dir(path)) {
		return m
	}
	li, err := os.Lstat(path)
	if err != nil || !cacheFileTrusted(li) {
		return m
	}
	f, err := os.Open(path)
	if err != nil {
		return m
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !os.SameFile(li, fi) { // swapped since Lstat
		return m
	}
	b, err := io.ReadAll(io.LimitReader(f, maxCompCacheBytes))
	if err != nil {
		return m
	}
	_ = json.Unmarshal(b, &m)
	return m
}

// writeCompCache stores one data set atomically (0600 temp file + rename) in a
// directory we own with mode 0700; otherwise it doesn't cache. If the
// directory was unsafe when found, every cache file in it is deleted rather
// than merged: tightening it must not launder files planted (or renamed over
// another key) while it was open.
func writeCompCache(path, set string, items []compEntry) {
	if path == "" {
		return
	}
	dir := filepath.Dir(path)
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	wasSafe := cacheDirSafe(dir)
	if !cacheDirUsable(dir) {
		return
	}
	m := map[string]compCacheEntry{}
	if wasSafe {
		m = readCompCache(path)
	} else {
		// Repairing an unsafe dir (now 0700, so no one else can add files):
		// drop every cache file in it — any could have been planted or
		// renamed over another backend's key while it was open.
		for _, pat := range []string{"completion-*.json", ".completion-*"} {
			old, _ := filepath.Glob(filepath.Join(dir, pat))
			for _, f := range old {
				_ = os.Remove(f)
			}
		}
	}
	m[set] = compCacheEntry{At: time.Now(), Items: items}
	b, err := json.Marshal(m)
	if err != nil {
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

var errCompletionSkip = errors.New("live completion skipped")

// completionData returns a data set from the fresh cache or the live API. Any
// failure (unreadable config, unreachable, TLS, auth, insecure, timeout)
// returns an empty result. Only flag values are read on this goroutine; all
// I/O happens in the bounded worker.
func (a *app) completionData(cmd *cobra.Command, set string) compResult {
	ov := a.overrides()
	timeout := completionTimeout()
	parent := cmd.Context()
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	ch := make(chan compResult, 1)
	go func() {
		r, err := loadCompletion(ctx, ov, set)
		if err != nil {
			r = compResult{}
		}
		ch <- r
	}()
	// Return on the deadline even if the worker is stuck in something that
	// ignores ctx (a FIFO config, a keyring stuck on D-Bus); the abandoned
	// worker dies with the completion process, which exits right after.
	select {
	case r := <-ch:
		return r
	case <-ctx.Done():
		return compResult{}
	}
}

func loadCompletion(ctx context.Context, ov config.Overrides, set string) (compResult, error) {
	f, err := config.Load(config.DefaultPath())
	if err != nil {
		return compResult{}, err
	}
	// Identity for the cache key and the insecure check: a placeholder secret
	// short-circuits every secret source (flag ref, env, keyring, inline).
	keyOv := ov
	keyOv.Secret, keyOv.SecretRef, keyOv.Warn = "-", "", io.Discard
	id, err := completionResolve(f, keyOv)
	if err != nil {
		return compResult{}, err
	}
	if id.Server == "" || (id.TLSInsecure && id.TLSFinger == "") {
		return compResult{}, errCompletionSkip
	}
	res := compResult{defaultRemote: id.Remote}
	path := completionCachePath(id)
	if e, ok := readCompCache(path)[set]; ok {
		if age := time.Since(e.At); age >= 0 && age < completionCacheTTL {
			res.items = e.Items
			return res, nil
		}
	}

	ov.Warn = io.Discard
	s, err := completionResolve(f, ov)
	if err != nil {
		return compResult{}, err
	}
	p, err := provider.New(s, false)
	if err != nil {
		return compResult{}, err
	}
	if w, ok := p.(interface{ SetWarnWriter(io.Writer) }); ok {
		w.SetWarnWriter(io.Discard)
	}
	items, err := fetchCompletion(ctx, p, set)
	if err != nil {
		return compResult{}, err
	}
	writeCompCache(path, set, items)
	res.items = items
	return res, nil
}

func fetchCompletion(ctx context.Context, p provider.Provider, set string) ([]compEntry, error) {
	out := []compEntry{}
	switch set {
	case compGuests, compTags:
		gs, err := p.ListGuests(ctx, provider.GuestFilter{})
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, g := range gs {
			if set == compGuests {
				out = append(out, compEntry{ID: strconv.Itoa(g.VMID), Name: g.Name, Kind: string(g.Kind), Node: g.Node, Remote: g.Remote, Status: g.Status})
				continue
			}
			for _, t := range splitTags(g.Tags) {
				if !seen[t] {
					seen[t] = true
					out = append(out, compEntry{ID: t})
				}
			}
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

// flagValue reads a flag's string value (local or inherited), "" when absent.
func flagValue(cmd *cobra.Command, name string) string {
	if f := cmd.Flag(name); f != nil {
		return f.Value.String()
	}
	return ""
}

// completeGuests suggests vmids (described by name) and, unless idsOnly, names
// (described by vmid) of the given kind ("" = both), narrowed by --node and by the
// effective remote (--remote > context `remote:`, as in resolveGuest) on PDM.
// Positional guest args take names (resolveGuest resolves them); --vmid flags
// take numeric ids only, so they use idsOnly.
func completeGuests(a *app, kind domain.GuestKind, idsOnly bool) completer {
	return func(cmd *cobra.Command, _ []string, tc string) ([]string, cobra.ShellCompDirective) {
		// Comma-list flags (e.g. `tag add --vmid 100,10<TAB>`): complete the
		// last element and keep what precedes it.
		lead := ""
		if i := strings.LastIndex(tc, ","); i >= 0 {
			lead, tc = tc[:i+1], tc[i+1:]
		}
		data := a.completionData(cmd, compGuests)
		node, remote := flagValue(cmd, "node"), flagValue(cmd, "remote")
		if remote == "" {
			remote = data.defaultRemote
		}
		var ids []compEntry
		names := map[string]string{}
		for _, g := range data.items {
			// Remote scoping applies to PDM data only (PVE guests carry none).
			if (kind != "" && g.Kind != string(kind)) || (node != "" && g.Node != node) ||
				(remote != "" && g.Remote != "" && !strings.EqualFold(g.Remote, remote)) {
				continue
			}
			if strings.HasPrefix(g.ID, tc) {
				ids = append(ids, g)
			}
			if !idsOnly && g.Name != "" && strings.HasPrefix(g.Name, tc) {
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
		for _, n := range a.completionData(cmd, compNodes).items {
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
		for _, e := range a.completionData(cmd, set).items {
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
		return completeGuests(a, guestKindFor(c), false)
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
				return nil, cobra.ShellCompDirectiveDefault // e.g. upload's <file>
			}
		}
	}

	flags := map[string]completer{
		"node":        completeNodes(a),
		"target-node": completeNodes(a),
		"remote":      completeIDs(a, compRemotes),
		"vmid":        completeGuests(a, guestKindFor(c), true),
		"storage":     completeIDs(a, compStorages),
	}
	if topLevel(c) == "remote" {
		delete(flags, "node") // `remote add/update --node` is an endpoint address
	}
	for name, fn := range flags {
		// c.Flag also finds the command's own (and inherited) persistent
		// flags; the completion registry is keyed by the *pflag.Flag, so the
		// declaring command registers once and its children skip.
		if c.Flag(name) != nil {
			if _, exists := c.GetFlagCompletionFunc(name); !exists {
				_ = c.RegisterFlagCompletionFunc(name, fn)
			}
		}
	}
	for _, sub := range c.Commands() {
		wireLiveCompletion(a, sub)
	}
}
