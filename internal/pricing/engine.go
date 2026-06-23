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

// CalculateEmergencySurcharge applies an extra fee if it's an SOS/Emergency
func (e *Engine) CalculateEmergencySurcharge(baseCost float64, isSOS bool) float64 {
	if !isSOS {
		return 0.0
	}
	return baseCost * (e.EmergencyMultiplier - 1.0)
}

// CalculateNightSurcharge applies an extra fee if it's currently night time (10 PM to 5 AM)
func (e *Engine) CalculateNightSurcharge(baseCost float64, currentTime time.Time) float64 {
	hour := currentTime.Hour()
	// Between 10 PM (22) and 5 AM (5)
	if hour >= 22 || hour < 5 {
		return baseCost * (e.NightMultiplier - 1.0)
	}
	return 0.0
}
