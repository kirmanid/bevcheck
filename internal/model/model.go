// Package model holds the API input contract shared across the server and the
// fixture generator. Mirrors TTB Form 5100.31 label-matchable fields.
package model

// Application mirrors the label-matchable fields of TTB Form 5100.31
// (research.txt 4.6), flattened to a single level: no nested applicant/expected
// objects. Class type / alcohol content / net contents / country of origin are
// label-derived; empty means "not asserted" (research.txt 4.7).
type Application struct {
	ProductType      string `json:"product_type"` // wine | distilled_spirits | malt_beverages
	Source           string `json:"source"`       // domestic | imported
	BrandName        string `json:"brand_name"`
	ApplicantName    string `json:"applicant_name"`
	ApplicantAddress string `json:"applicant_address"`
	ClassType        string `json:"class_type,omitempty"`
	AlcoholContent   string `json:"alcohol_content,omitempty"`
	NetContents      string `json:"net_contents,omitempty"`
	CountryOfOrigin  string `json:"country_of_origin,omitempty"`

	// Conditional disclosures (27 CFR 5.63(c), 4.32(c-e), 7.63(b)): asserted
	// when the product contains/uses the substance, in which case the label
	// must carry the mandated statement (research.txt 8.11).
	ContainsNeutralSpirits bool   `json:"contains_neutral_spirits,omitempty"` // (c)(1)
	Colored                bool   `json:"colored,omitempty"`                  // (c)(2)
	AgeStatement           string `json:"age_statement,omitempty"`            // (c)(3)
	StateOfDistillation    string `json:"state_of_distillation,omitempty"`    // (c)(4)
	ContainsFDACYellow5    bool   `json:"contains_fdc_yellow5,omitempty"`     // (c)(5)
	ContainsCarmine        bool   `json:"contains_carmine,omitempty"`         // (c)(6)
	ContainsSulfites       bool   `json:"contains_sulfites,omitempty"`        // (c)(7)
	ContainsAspartame      bool   `json:"contains_aspartame,omitempty"`       // (c)(8)
}

// BatchItem is one line of a JSONL batch submission: the flat Application
// fields plus an image_url to download. encoding/json inlines the embedded
// Application, so the wire shape stays flat (no nested objects).
type BatchItem struct {
	Application
	ImageURL string `json:"image_url"`
}
