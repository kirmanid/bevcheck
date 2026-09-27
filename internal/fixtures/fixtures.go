// Package fixtures holds the canonical "good label" and "imported label"
// definitions shared by the unit tests and the genfixtures command, so the
// two cannot drift apart.
package fixtures

import (
	"Bevcheck/internal/labelgen"
	"Bevcheck/internal/model"
)

// GoodFields is the canonical compliant distilled-spirits label content.
func GoodFields() labelgen.Fields {
	return labelgen.Fields{
		BrandName:      "OLD TOM DISTILLERY",
		ClassType:      "Kentucky Straight Bourbon Whiskey",
		AlcoholContent: "45% Alc./Vol. (90 Proof)",
		NetContents:    "750 mL",
		NameAddress:    "Bottled by Old Tom Distillery Co., Bardstown, KY",
	}
}

// GoodApp is the matching 5100.31 application for GoodFields.
func GoodApp() model.Application {
	return model.Application{
		ProductType:      "distilled_spirits",
		Source:           "domestic",
		BrandName:        "OLD TOM DISTILLERY",
		ApplicantName:    "Old Tom Distillery Co.",
		ApplicantAddress: "Bardstown, KY",
		ClassType:        "Kentucky Straight Bourbon Whiskey",
		AlcoholContent:   "45%",
		NetContents:      "750 mL",
	}
}

// ImportedFields is the compliant imported distilled-spirits label content.
func ImportedFields() labelgen.Fields {
	return labelgen.Fields{
		BrandName:       "OLD TOM DISTILLERY",
		ClassType:       "Kentucky Straight Bourbon Whiskey",
		AlcoholContent:  "45% Alc./Vol. (90 Proof)",
		NetContents:     "750 mL",
		NameAddress:     "Imported by Old Tom Import Co., Glasgow, Scotland",
		CountryOfOrigin: "Product of Scotland",
	}
}

// ImportedApp is the matching application for ImportedFields, with the country
// of origin asserted only when withOrigin is true.
func ImportedApp(withOrigin bool) model.Application {
	a := model.Application{
		ProductType:      "distilled_spirits",
		Source:           "imported",
		BrandName:        "OLD TOM DISTILLERY",
		ApplicantName:    "Old Tom Import Co.",
		ApplicantAddress: "Glasgow, Scotland",
		ClassType:        "Kentucky Straight Bourbon Whiskey",
		AlcoholContent:   "45%",
		NetContents:      "750 mL",
	}
	if withOrigin {
		a.CountryOfOrigin = "Scotland"
	}
	return a
}
