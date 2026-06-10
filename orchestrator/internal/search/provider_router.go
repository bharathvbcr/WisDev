package search

import (
	"context"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/resilience"
)

// ProviderRouter decides which search providers to use for a given query.
type ProviderRouter struct {
	intelligence *SearchIntelligence
	registry     *ProviderRegistry
}

func NewProviderRouter(intelligence *SearchIntelligence, registry *ProviderRegistry) *ProviderRouter {
	return &ProviderRouter{
		intelligence: intelligence,
		registry:     registry,
	}
}

// Route selects the best providers for the given query and domain.
func (pr *ProviderRouter) Route(ctx context.Context, query string, domain string) []SearchProvider {
	if pr == nil || pr.registry == nil {
		return nil
	}

	normalizedDomain := NormalizeRoutingDomain(domain)
	if normalizedDomain == "" || normalizedDomain == "general" {
		if inferred := InferDomainFromQuery(query); inferred != "" && inferred != "general" {
			normalizedDomain = inferred
		}
	}
	if normalizedDomain == "" {
		normalizedDomain = "general"
	}

	domainProviders := pr.registry.ProvidersFor(normalizedDomain)
	allowedByDomain := make(map[string]struct{}, len(domainProviders))
	for _, provider := range domainProviders {
		allowedByDomain[provider.Name()] = struct{}{}
	}

	if pr.intelligence != nil {
		topProviders, err := pr.intelligence.GetTopProviders(ctx, normalizedDomain, 5)
		if err == nil && len(topProviders) > 0 {
			selected := pr.lookupProviders(topProviders, normalizedDomain, allowedByDomain)
			if len(selected) > 0 {
				return selected
			}
		}
	}

	if len(domainProviders) > 0 {
		return domainProviders
	}

	return pr.lookupProviders(nil, "", nil)
}

func (pr *ProviderRouter) lookupProviders(names []string, domain string, allowedByDomain map[string]struct{}) []SearchProvider {
	pr.registry.mu.RLock()
	defer pr.registry.mu.RUnlock()

	selected := make([]SearchProvider, 0, len(names))
	seen := make(map[string]struct{}, len(names))

	includeProvider := func(name string) {
		name = NormalizeProviderName(name)
		if name == "" {
			return
		}
		if _, alreadyIncluded := seen[name]; alreadyIncluded {
			return
		}
		if domain != "" && domain != "general" && len(allowedByDomain) > 0 {
			if _, allowed := allowedByDomain[name]; !allowed {
				return
			}
		}
		provider, ok := pr.registry.providers[name]
		if !ok || !provider.Healthy() {
			return
		}
		if breaker := pr.registry.breakers[name]; breaker != nil && breaker.State() == resilience.StateOpen {
			return
		}
		seen[name] = struct{}{}
		selected = append(selected, provider)
	}

	if len(names) == 0 {
		for _, name := range pr.registry.defaults {
			includeProvider(name)
		}
		return selected
	}

	for _, name := range names {
		includeProvider(name)
	}
	return selected
}

// SelectByQueryType uses keywords in the query to suggest providers.
func (pr *ProviderRouter) SelectByQueryType(query string) string {
	return InferDomainFromQuery(query)
}

func inferDomainFromQuery(query string) string {
	return InferDomainFromQuery(query)
}
