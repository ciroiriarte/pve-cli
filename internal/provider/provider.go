// Package provider abstracts the backend (direct PVE cluster or PDM). The CLI
// layer depends only on this interface and on domain types, never on raw HTTP.
package provider

import (
	"context"
	"fmt"
	"net/url"

	"github.com/ciroiriarte/pve-cli/internal/auth"
	"github.com/ciroiriarte/pve-cli/internal/config"
	"github.com/ciroiriarte/pve-cli/internal/domain"
	"github.com/ciroiriarte/pve-cli/internal/protocol"
	"github.com/ciroiriarte/pve-cli/internal/transport"
)

// Capabilities declares which command groups a provider supports, used to gate
// command registration and help.
type Capabilities struct {
	Remotes bool // PDM aggregates clusters via "remotes"
}

// GuestFilter narrows guest listings.
type GuestFilter struct {
	Kind   domain.GuestKind // "" = both vms and containers
	Node   string           // "" = all nodes
	Status string           // "" = any
}

// LogOptions controls task-log retrieval.
type LogOptions struct {
	Start int // first line to return (1-based); 0 = from the beginning
	Limit int // max lines; 0 = server default
}

// Provider is the backend contract consumed by the CLI.
type Provider interface {
	Name() string
	Capabilities() Capabilities

	ListNodes(ctx context.Context) ([]domain.Node, error)
	ListGuests(ctx context.Context, f GuestFilter) ([]domain.Guest, error)
	// ResolveGuest finds which node hosts a vmid and its kind.
	ResolveGuest(ctx context.Context, vmid int) (domain.Guest, error)
	// GuestPower performs a lifecycle action (start/stop/shutdown/reboot/...).
	GuestPower(ctx context.Context, g domain.Guest, action string) (protocol.TaskHandle, error)
	// GuestConfig returns the raw config map for a guest.
	GuestConfig(ctx context.Context, g domain.Guest) (map[string]any, error)

	TaskStatus(ctx context.Context, h protocol.TaskHandle) (protocol.TaskStatus, error)
	ListTasks(ctx context.Context, node string) ([]protocol.TaskStatus, error)
	// TaskLog returns task log lines starting at line opts.Start (1-based).
	TaskLog(ctx context.Context, h protocol.TaskHandle, opts LogOptions) ([]protocol.LogLine, error)

	// Raw issues an arbitrary API call (backs `pc api`).
	Raw(ctx context.Context, method, path string, params url.Values) ([]byte, error)
}

// New builds a Provider from resolved settings.
func New(s *config.Settings, debug bool) (Provider, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}

	tlsCfg := transport.TLSConfig{
		CAFile:      s.TLSCAFile,
		Fingerprint: s.TLSFinger,
		Insecure:    s.TLSInsecure,
	}

	var ap auth.Provider
	switch s.AuthType {
	case "token":
		t, err := auth.NewToken(s.TokenID, s.Secret)
		if err != nil {
			return nil, err
		}
		ap = t
	case "ticket":
		hc, err := transport.NewHTTPClient(tlsCfg, 0)
		if err != nil {
			return nil, err
		}
		t, err := auth.NewTicket(s.Server, s.User, s.Secret, hc)
		if err != nil {
			return nil, err
		}
		ap = t
	default:
		return nil, fmt.Errorf("unsupported auth type %q", s.AuthType)
	}

	cl, err := transport.New(transport.Options{
		BaseURL:   s.Server,
		Auth:      ap,
		TLS:       tlsCfg,
		Debug:     debug,
		UserAgent: "pve-cli",
		RateQPS:   s.RateQPS,
		Burst:     s.RateBurst,
	})
	if err != nil {
		return nil, err
	}

	switch s.Provider {
	case "pve", "":
		return newPVE(cl), nil
	case "pdm":
		return nil, fmt.Errorf("the pdm provider is not yet implemented (planned for M4)")
	default:
		return nil, fmt.Errorf("unknown provider %q", s.Provider)
	}
}

// pveConstructor is wired by the pve subpackage via SetPVEFactory to avoid an
// import cycle while keeping New as the single entry point.
var pveConstructor func(*transport.Client) Provider

// SetPVEFactory registers the PVE provider constructor.
func SetPVEFactory(f func(*transport.Client) Provider) { pveConstructor = f }

func newPVE(cl *transport.Client) Provider {
	if pveConstructor == nil {
		panic("pve provider factory not registered")
	}
	return pveConstructor(cl)
}
