package wisdev

import "strings"

type ServiceTier string

const (
	ServiceTierStandard ServiceTier = "standard"
	ServiceTierPriority ServiceTier = "priority"
	ServiceTierFlex     ServiceTier = "flex"
)

func NormalizeServiceTier(raw string) ServiceTier {
	switch ServiceTier(strings.ToLower(strings.TrimSpace(raw))) {
	case ServiceTierStandard:
		return ServiceTierStandard
	case ServiceTierPriority:
		return ServiceTierPriority
	case ServiceTierFlex:
		return ServiceTierFlex
	default:
		return ""
	}
}

func ResolveLoopServiceTier(mode WisDevMode, interactive bool, requested ServiceTier) ServiceTier {
	if tier := NormalizeServiceTier(string(requested)); tier != "" {
		return tier
	}
	return ResolveServiceTier(mode, interactive)
}
