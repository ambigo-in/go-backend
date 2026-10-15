package pricing

import (
	"sort"
	"time"
)

type PricingTier struct {
	ThresholdDistance float64 `bson:"threshold_distance" json:"threshold_distance"`
	CostPerKm         float64 `bson:"cost_per_km" json:"cost_per_km"`
}

type Engine struct {
	// Kept as construction-time defaults; per-request values come from
	// pricing_settings via SettingsStore and are passed explicitly to the
	// Calculate* methods below.
	EmergencyMultiplier float64
	NightMultiplier     float64
}

func NewEngine() *Engine {
	return &Engine{
		EmergencyMultiplier: 1.5, // 50% extra for high-priority emergencies
		NightMultiplier:     1.2, // 20% extra for night time
	}
}

// CalculateBaseAndDistanceFare ports the exact tiered pricing algorithm from V1
func (e *Engine) CalculateBaseAndDistanceFare(distanceKm float64, baseFare float64, tiers []PricingTier) float64 {
	// Ensure tiers are sorted by ThresholdDistance ascending
	sort.Slice(tiers, func(i, j int) bool {
		return tiers[i].ThresholdDistance < tiers[j].ThresholdDistance
	})

	totalCost := 0.0
	previousThreshold := 0.0

	for _, tier := range tiers {
		if distanceKm <= previousThreshold {
			break // No more distance to charge
		}

		applicableDistance := distanceKm
		if distanceKm > tier.ThresholdDistance {
			applicableDistance = tier.ThresholdDistance
		}
		
		chargeableDistance := applicableDistance - previousThreshold
		totalCost += chargeableDistance * tier.CostPerKm
		previousThreshold = tier.ThresholdDistance
	}

	return totalCost + baseFare
}

// CalculateEmergencySurcharge applies an extra fee if it's an SOS/Emergency.
// mult is the engine multiplier (1.5 = +50%); resolved per request from
// pricing_settings so admins can configure it without a deploy.
func (e *Engine) CalculateEmergencySurcharge(baseCost float64, isSOS bool, mult float64) float64 {
	if !isSOS {
		return 0.0
	}
	if mult < 1.0 {
		mult = 1.0
	}
	return baseCost * (mult - 1.0)
}

// CalculateNightSurcharge applies an extra fee if it's currently night time
// (10 PM to 5 AM). mult is the engine multiplier (1.2 = +20%).
func (e *Engine) CalculateNightSurcharge(baseCost float64, currentTime time.Time, mult float64) float64 {
	hour := currentTime.Hour()
	// Between 10 PM (22) and 5 AM (5)
	if hour >= 22 || hour < 5 {
		if mult < 1.0 {
			mult = 1.0
		}
		return baseCost * (mult - 1.0)
	}
	return 0.0
}
