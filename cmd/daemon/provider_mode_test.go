package main

import (
	"context"
	"testing"

	"github.com/zatca-go/zatca/config"
	"github.com/zatca-go/zatca/models"
	"github.com/zatca-go/zatca/provider"
)

func TestParseProviderModeDefaultsToReal(t *testing.T) {
	for _, raw := range []string{"", "real", " REAL "} {
		got, err := parseProviderMode(raw)
		if err != nil {
			t.Fatalf("parseProviderMode(%q) error: %v", raw, err)
		}
		if got != providerModeReal {
			t.Fatalf("parseProviderMode(%q) = %q; want %q", raw, got, providerModeReal)
		}
	}
}

func TestParseProviderModeRejectsUnknownMode(t *testing.T) {
	if _, err := parseProviderMode("sandbox"); err == nil {
		t.Fatal("parseProviderMode(\"sandbox\") should fail")
	}
}

func TestRealProviderModeLeavesFactoryUnset(t *testing.T) {
	factory, err := newProviderFactory("", "not-a-local-scenario")
	if err != nil {
		t.Fatalf("newProviderFactory(real) error: %v", err)
	}
	if factory != nil {
		t.Fatal("real provider mode should leave the factory nil")
	}
}

func TestLocalProviderModeUsesCredentialFreeCachedProvider(t *testing.T) {
	factory, err := newProviderFactory("local", string(provider.ScenarioAccepted))
	if err != nil {
		t.Fatalf("newProviderFactory(local) error: %v", err)
	}
	cfg := &config.Config{}
	first, err := factory(7, nil, cfg)
	if err != nil {
		t.Fatalf("first provider = %v", err)
	}
	second, err := factory(7, nil, cfg)
	if err != nil {
		t.Fatalf("second provider = %v", err)
	}
	if first != second {
		t.Fatal("local provider factory should reuse one adapter per branch")
	}
	if _, ok := first.(*provider.Local); !ok {
		t.Fatalf("provider = %T; want *provider.Local", first)
	}
	if cfg.ProductionUsername != "" || cfg.ProductionPassword != "" ||
		cfg.ComplianceUsername != "" || cfg.CompliancePassword != "" {
		t.Fatalf("local provider mode fabricated credentials: %+v", cfg)
	}
}

func TestLocalProviderModeRejectsInvalidScenario(t *testing.T) {
	if _, err := newProviderFactory("local", "not-a-scenario"); err == nil {
		t.Fatal("invalid local provider scenario should fail")
	}
}

func TestLocalProviderFactoryAdapterSubmitsWithoutCredentials(t *testing.T) {
	factory, err := newProviderFactory("local", string(provider.ScenarioAccepted))
	if err != nil {
		t.Fatalf("newProviderFactory(local) error: %v", err)
	}
	adapter, err := factory(7, nil, &config.Config{})
	if err != nil {
		t.Fatalf("factory = %v", err)
	}
	_, err = adapter.Submit(context.Background(), provider.Submission{
		Endpoint: "https://example.invalid",
		Kind:     provider.KindStandard,
		Request: models.InvoiceRequest{
			UUID:        "0c4f7a1e-2b3d-4e5f-8a9b-1c2d3e4f5a6b",
			InvoiceHash: "hash",
			Invoice:     "<Invoice/>",
		},
		SignedXML: "<Invoice/>",
	})
	if err != nil {
		t.Fatalf("local provider submit = %v", err)
	}
}
