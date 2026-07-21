package policy

// Provider-tier policy — canonical Go owner of "which academic search providers
// are unlocked at which access tier". Ported from API_PROVIDERS + getProvidersByTier
// in frontend/lib/apiProviders.ts as part of Phase 1 of the frontend-thinning
// migration.
//
// SCOPE NOTE: this file owns the tier->provider *policy* (identifiers + gating).
// The concrete provider HTTP clients, rate-limit/backoff, and fan-out execution
// move to internal/search in Phase 2 (search orchestration). Provider identifiers
// here are the canonical IDs those clients must register under.

// ProviderTier is the access tier (1..4) at which a provider becomes available.
// A quality mode's QualityModeFeatures.APITierAccess unlocks every provider whose
// tier is <= that value.
type ProviderTier int

// ProviderConfig is the minimal, transport-free descriptor of an academic search
// provider. Display metadata (name, description, homepage) stays in the frontend;
// only what the backend needs to gate and fan out lives here.
type ProviderConfig struct {
	ID   string       `json:"id"`
	Tier ProviderTier `json:"tier"`
}

// apiProviders preserves the declaration order of API_PROVIDERS in
// frontend/lib/apiProviders.ts so that tier queries return providers in the same
// order the browser used (order parity keeps ranking/fan-out deterministic during
// the migration).
var apiProviders = []ProviderConfig{
	{ID: "opensearch_hybrid", Tier: 1},
	{ID: "semanticscholar", Tier: 1},
	{ID: "openalex", Tier: 1},
	{ID: "crossref", Tier: 2},
	{ID: "core", Tier: 2},
	{ID: "arxiv", Tier: 3},
	{ID: "dblp", Tier: 3},
	{ID: "paperswithcode", Tier: 3},
	{ID: "europepmc", Tier: 4},
	{ID: "biorxiv", Tier: 4},
	{ID: "pubmed", Tier: 4},
	{ID: "openCitations", Tier: 3},
	{ID: "base", Tier: 3},
	{ID: "ssrn", Tier: 3},
	{ID: "clinicalTrials", Tier: 3},
	{ID: "repec", Tier: 2},
	{ID: "patentsview", Tier: 3},
	{ID: "doaj", Tier: 3},
	{ID: "dimensions", Tier: 2},
	{ID: "orcid", Tier: 3},
	{ID: "nasaads", Tier: 4},
	{ID: "philpapers", Tier: 4},
	{ID: "ieee", Tier: 4},
	{ID: "scopus", Tier: 4},
}

// ProvidersAtTier returns providers whose tier equals exactly the given tier.
// Parity with getProvidersByTier(tier) in frontend/lib/apiProviders.ts.
func ProvidersAtTier(tier ProviderTier) []ProviderConfig {
	out := make([]ProviderConfig, 0, len(apiProviders))
	for _, p := range apiProviders {
		if p.Tier == tier {
			out = append(out, p)
		}
	}
	return out
}

// ProvidersUpToTier returns every provider unlocked at an access level, i.e. all
// providers whose tier is <= access, preserving declaration order. This is the
// cumulative set a quality mode's APITierAccess grants.
func ProvidersUpToTier(access int) []ProviderConfig {
	out := make([]ProviderConfig, 0, len(apiProviders))
	for _, p := range apiProviders {
		if int(p.Tier) <= access {
			out = append(out, p)
		}
	}
	return out
}

// ProvidersForSubscription resolves the provider set a subscription tier can use,
// by mapping the tier -> its default quality mode -> that mode's APITierAccess.
// This is the payload for GET /api/config/providers when no explicit access tier
// is supplied.
func ProvidersForSubscription(tier SubscriptionTier) []ProviderConfig {
	mode := DefaultQualityMode(tier)
	cfg, ok := qualityModes[mode]
	if !ok {
		return nil
	}
	return ProvidersUpToTier(cfg.Features.APITierAccess)
}

// ProviderIDs is a convenience that flattens configs to their canonical IDs,
// preserving order.
func ProviderIDs(configs []ProviderConfig) []string {
	ids := make([]string, 0, len(configs))
	for _, c := range configs {
		ids = append(ids, c.ID)
	}
	return ids
}
