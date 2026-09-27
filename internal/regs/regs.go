// Package regs holds regulatory constants and rules quoted in research.txt,
// sourced from 27 CFR Parts 4, 5, 7, and 16.
package regs

// GovernmentWarning is the verbatim statement required by 27 CFR 16.21.
const GovernmentWarning = `GOVERNMENT WARNING: (1) According to the Surgeon General, women should not drink alcoholic beverages during pregnancy because of the risk of birth defects. (2) Consumption of alcoholic beverages impairs your ability to drive a car or operate machinery, and may cause health problems.`

// AspartameDisclosure is the verbatim, all-caps statement required by 27 CFR
// 5.63(c)(8) when a product contains aspartame.
const AspartameDisclosure = "PHENYLKETONURICS: CONTAINS PHENYLALANINE"

// Malt-beverage designation thresholds (27 CFR 7.65): "non-alcoholic"/"alcohol
// free" require <0.5% ABV, "low alcohol" requires <2.5% ABV.
const (
	MaltNonAlcoholicMaxABV = 0.5
	MaltLowAlcoholMaxABV   = 2.5
)

// AlcoholTolerance returns the allowed ABV tolerance in percentage points:
//   - distilled spirits: 27 CFR 5.65(c) -> +/- 0.3
//   - malt beverages:    27 CFR 7.65(c) -> +/- 0.3
//   - wine:              27 CFR 4.36(b) -> +/- 1.0 over 14%, else +/- 1.5
func AlcoholTolerance(productType string, abv float64) float64 {
	switch productType {
	case "wine":
		if abv > 14.0 {
			return 1.0
		}
		return 1.5
	default: // distilled_spirits, malt_beverages, unknown
		return 0.3
	}
}
