package main

import (
	"fmt"
	"strings"
	"sync"

	"github.com/zatca-go/zatca/config"
	"github.com/zatca-go/zatca/processor"
	"github.com/zatca-go/zatca/provider"
	"github.com/zatca-go/zatca/zatca"
)

type providerMode string

const (
	providerModeReal  providerMode = "real"
	providerModeLocal providerMode = "local"
)

func parseProviderMode(raw string) (providerMode, error) {
	mode := providerMode(strings.ToLower(strings.TrimSpace(raw)))
	if mode == "" {
		return providerModeReal, nil
	}
	switch mode {
	case providerModeReal, providerModeLocal:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid provider mode %q (must be real or local)", raw)
	}
}

// newProviderFactory returns nil for the real provider so BillPipeline keeps
// using the branch's configured client. Local mode owns one credential-free
// adapter per branch, which preserves deterministic state across redelivery.
func newProviderFactory(rawMode, rawScenario string) (processor.ProviderFactory, error) {
	mode, err := parseProviderMode(rawMode)
	if err != nil {
		return nil, err
	}
	if mode == providerModeReal {
		return nil, nil
	}

	scenario, err := provider.ParseSubmitScenario(rawScenario)
	if err != nil {
		return nil, err
	}

	var mu sync.Mutex
	localByBranch := make(map[int64]*provider.Local)
	return func(branchID int64, _ *zatca.Service, _ *config.Config) (provider.Adapter, error) {
		if branchID <= 0 {
			return nil, fmt.Errorf("local provider branch ID must be positive")
		}
		mu.Lock()
		defer mu.Unlock()
		if local := localByBranch[branchID]; local != nil {
			return local, nil
		}
		local := provider.NewLocal(provider.LocalOptions{SubmitScenario: scenario})
		localByBranch[branchID] = local
		return local, nil
	}, nil
}
