package scrapers_test

import (
	"context"
	"testing"

	"ExpeditusClient/internal/api"
	"ExpeditusClient/internal/config"
	"ExpeditusClient/internal/scrapers"

	delfosprov "ExpeditusClient/internal/scrapers/delfos"
	"ExpeditusClient/internal/scrapers/tiptravel"
)

func TestProviderForOrderDefaultsToDelfos(t *testing.T) {
	if got := scrapers.ProviderForOrder(nil); got != scrapers.ProviderDelfos {
		t.Errorf("ProviderForOrder(nil) = %q, want %q", got, scrapers.ProviderDelfos)
	}
	if got := scrapers.ProviderForOrder(&api.ScrapingOrder{}); got != scrapers.ProviderDelfos {
		t.Errorf("ProviderForOrder(empty) = %q, want %q", got, scrapers.ProviderDelfos)
	}
	if got := scrapers.ProviderForOrder(&api.ScrapingOrder{Provider: "tiptravel"}); got != "tiptravel" {
		t.Errorf("ProviderForOrder(tiptravel) = %q, want %q", got, "tiptravel")
	}
}

func TestRegistryRegistersBuiltins(t *testing.T) {
	// Set passTip so tiptravel.Register() does not bail out.
	t.Setenv("passTip", "test-password")
	delfosprov.Register()
	if err := tiptravel.Register(); err != nil {
		t.Fatalf("tiptravel register: %v", err)
	}
	names := scrapers.Names()
	want := map[string]bool{scrapers.ProviderDelfos: false, scrapers.ProviderTipTravel: false}
	for _, n := range names {
		if _, ok := want[n]; ok {
			want[n] = true
		}
	}
	for n, found := range want {
		if !found {
			t.Errorf("expected provider %q to be registered (have: %v)", n, names)
		}
	}
}

func TestRegistryLookupUnknown(t *testing.T) {
	_, err := scrapers.Lookup("does-not-exist")
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestRegistryLookupKnown(t *testing.T) {
	t.Setenv("passTip", "test-password")
	delfosprov.Register()
	_ = tiptravel.Register()

	// delfos: the factory expects a legacy runner to be set. We just
	// verify the lookup works; building would require injecting the
	// runner, which is outside the unit-test scope.
	if factory, err := scrapers.Lookup(scrapers.ProviderDelfos); err != nil {
		t.Errorf("Lookup(delfos) failed: %v", err)
	} else if factory == nil {
		t.Error("delfos factory should not be nil")
	}

	// tiptravel: full factory build succeeds because we set passTip.
	if factory, err := scrapers.Lookup(scrapers.ProviderTipTravel); err != nil {
		t.Fatalf("Lookup(tiptravel) failed: %v", err)
	} else {
		cfg := &config.LoginConfig{}
		provider, err := factory(cfg)
		if err != nil {
			t.Fatalf("tiptravel factory build failed: %v", err)
		}
		if provider.Name() != scrapers.ProviderTipTravel {
			t.Errorf("Name() = %q, want %q", provider.Name(), scrapers.ProviderTipTravel)
		}
		// Run signature check only - we don't want to actually hit
		// tiptravelya.com in unit tests.
		var s *scrapers.ScraperSession
		_ = provider.Run(context.Background(), s, nil)
	}
}