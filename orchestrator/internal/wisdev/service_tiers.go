package wisdev

type ServiceTier string

const (
	ServiceTierStandard ServiceTier = "standard"
	ServiceTierPriority ServiceTier = "priority"
	ServiceTierFlex     ServiceTier = "flex"
)
