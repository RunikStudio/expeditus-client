// Package delfos wraps the legacy monolithic delfos scraper behind the
// ScraperProvider interface. The actual delfos-specific logic lives in
// internal/web/services/scraper.go and is invoked via the LegacyRunner
// interface to avoid breaking the existing 2961-line implementation.
package delfos

import (
	"context"
	"fmt"

	"ExpeditusClient/internal/api"
	"ExpeditusClient/internal/config"
	"ExpeditusClient/internal/scrapers"
)

// Name is the provider identifier. Exposed as a const so callers can
// reference it without depending on the scrapers package.
const Name = scrapers.ProviderDelfos

// LegacyRunner is the bridge to the existing delfos implementation. The
// ScraperService supplies a thin adapter that exposes only the methods
// this provider needs.
type LegacyRunner interface {
	RunScrapingLegacy(ctx context.Context, sessionID string, cfg *config.LoginConfig) error
	RunScrapingWithOrderLegacy(ctx context.Context, sessionID string, cfg *config.LoginConfig, order *api.ScrapingOrder) error
}

// Runner is a process-wide setter the ScraperService uses to inject
// the legacy runner once at startup. Set by services.ScraperService
// during construction.
var Runner LegacyRunner

// Register adds the delfos provider to the scrapers registry. Must be
// called once at startup (the binary does this from cmd/expeditus-web).
func Register() {
	scrapers.Register(Name, func(cfg *config.LoginConfig) (scrapers.ScraperProvider, error) {
		if Runner == nil {
			return nil, fmt.Errorf("delfos: legacy runner not initialized")
		}
		return &Provider{cfg: cfg, runner: Runner}, nil
	})
}

// Provider implements ScraperProvider for delfos.tur.ar by delegating
// to the legacy scraping functions.
type Provider struct {
	cfg    *config.LoginConfig
	runner LegacyRunner
}

// Name returns "delfos".
func (p *Provider) Name() string { return Name }

// LoginURL returns the entry point URL for the delfos login flow.
func (p *Provider) LoginURL() string {
	if p.cfg != nil && p.cfg.TargetURL != "" {
		return p.cfg.TargetURL
	}
	return "https://www.delfos.tur.ar/"
}

// Run executes the delfos scraping flow. It honors the order when one
// is provided and falls back to the no-order legacy mode otherwise.
func (p *Provider) Run(ctx context.Context, session *scrapers.ScraperSession, order *api.ScrapingOrder) error {
	if session == nil {
		return fmt.Errorf("delfos: nil session")
	}
	if order != nil && (order.SearchURL != "" || order.HotelID != "") {
		return p.runner.RunScrapingWithOrderLegacy(ctx, session.ID, p.cfg, order)
	}
	return p.runner.RunScrapingLegacy(ctx, session.ID, p.cfg)
}