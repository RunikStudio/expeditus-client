// Package scrapers defines the multi-provider scraper abstraction.
//
// Each provider (delfos, tiptravel, ...) implements the ScraperProvider
// interface and registers itself in the global Registry via an init().
//
// The ScraperService in internal/web/services delegates each session to
// the provider selected by the order's Provider field (default "delfos"
// for backwards compatibility).
package scrapers

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"ExpeditusClient/internal/api"
	"ExpeditusClient/internal/config"
)

// Provider names. Use these constants instead of raw strings everywhere.
const (
	ProviderDelfos    = "delfos"
	ProviderTipTravel = "tiptravel"
)

// ScraperProvider is the contract every microsite scraper must implement.
//
// A provider owns the full lifecycle for one order: login, search,
// room/price extraction and any 2FA handling specific to the site.
//
// Implementations should be safe for concurrent use by the ScraperService
// but each session only invokes a single provider at a time.
type ScraperProvider interface {
	// Name returns the unique provider identifier (e.g. "delfos").
	Name() string

	// LoginURL returns the URL the provider navigates to before logging in.
	LoginURL() string

	// Run executes the full scraping flow for the given order. It must:
	//   - perform login (interactive, may block for 2FA)
	//   - navigate to the search URL
	//   - extract prices for the requested hotel/room/meal plan
	//   - report progress via the Reporter
	//   - never panic; return a meaningful error on failure
	Run(ctx context.Context, session *ScraperSession, order *api.ScrapingOrder) error
}

// ScraperSession is the minimal subset of the session state that providers
// need to mutate. It is intentionally a thin wrapper so providers don't
// depend on the web service layer.
type ScraperSession struct {
	ID          string
	Order       *api.ScrapingOrder
	BookedPrice float64
	Ctx         context.Context
	Cancel      context.CancelFunc

	// Progress and result channels for the web UI.
	Reporter Reporter

	// 2FA channels: providers that need a code fill these and wait on them.
	TwoFACode chan string
	TwoFAErr  chan error

	// TwoFAInputSelector / TwoFASubmitSelector are set by providers that
	// detect a 2FA prompt and want to coordinate with the UI.
	TwoFAInputSelector  string
	TwoFASubmitSelector string

	// Browser context factory provided by the ScraperService.
	NewBrowser NewBrowserFunc
}

// NewBrowserFunc is the signature for spawning a chromedp browser context
// rooted at the session's context.
type NewBrowserFunc func(parent context.Context) (context.Context, context.CancelFunc)

// Reporter abstracts the progress/result sink. The web ScraperService
// supplies an implementation that translates to WebSocket events.
type Reporter interface {
	Progress(percent float64, stage, message string)
	Error(err error)
	Result(id string, data map[string]interface{})
	Needs2FA(inputSel, submitSel string) bool
	Submit2FA(code string) error
}

// Registry maps provider names to their factory functions. Factories
// receive the login config (credentials) and return a fresh provider.
type Registry struct {
	mu        sync.RWMutex
	factories map[string]FactoryFn
}

// FactoryFn builds a provider instance with the given login credentials.
type FactoryFn func(cfg *config.LoginConfig) (ScraperProvider, error)

var globalRegistry = &Registry{factories: make(map[string]FactoryFn)}

// Register adds a provider factory to the global registry. Typically
// called from the provider package's init(). Idempotent: re-registering
// the same name replaces the previous factory, which is convenient for
// tests and harmless in production.
func Register(name string, factory FactoryFn) {
	globalRegistry.mu.Lock()
	defer globalRegistry.mu.Unlock()
	if _, exists := globalRegistry.factories[name]; exists {
		return
	}
	globalRegistry.factories[name] = factory
}

// Lookup returns the provider for the given name or an error if unknown.
func Lookup(name string) (FactoryFn, error) {
	globalRegistry.mu.RLock()
	defer globalRegistry.mu.RUnlock()
	f, ok := globalRegistry.factories[name]
	if !ok {
		return nil, fmt.Errorf("scrapers: unknown provider %q (registered: %s)", name, strings.Join(Names(), ", "))
	}
	return f, nil
}

// Names returns the list of registered provider names, sorted.
func Names() []string {
	globalRegistry.mu.RLock()
	defer globalRegistry.mu.RUnlock()
	out := make([]string, 0, len(globalRegistry.factories))
	for n := range globalRegistry.factories {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ProviderForOrder resolves the provider name from the order, defaulting
// to delfos when empty (backwards compatible with existing data).
func ProviderForOrder(order *api.ScrapingOrder) string {
	if order == nil || order.Provider == "" {
		return ProviderDelfos
	}
	return order.Provider
}