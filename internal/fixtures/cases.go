package fixtures

import (
	"Bevcheck/internal/labelgen"
	"Bevcheck/internal/model"
)

// Case is one end-to-end scenario: a rendered label (Fields + Defects) paired
// with its application, and the verdict the verifier must reach. Shared by the
// genfixtures command (writes testdata/) and the server's testdata test (which
// re-derives the verdict), so the two cannot drift apart.
type Case struct {
	Name     string
	Desc     string
	Expected string // "Approved" | "Rejected" | "Needs Correction"
	Fields   labelgen.Fields
	Defects  labelgen.Defects
	App      model.Application
}

// Cases returns every scenario, ordered with the core field checks first and
// the conditional-disclosure / designation criteria after.
func Cases() []Case {
	cases := []Case{
		// --- core field checks -------------------------------------------------
		{Name: "good", Desc: "fully compliant distilled-spirits label", Expected: "Approved",
			Fields: GoodFields(), App: GoodApp()},
		{Name: "case-variant-brand", Desc: "brand asserted in a different case (fuzzy match, must NOT false-negative)", Expected: "Approved",
			Fields: GoodFields(), App: func() model.Application {
				a := GoodApp()
				a.BrandName = "old tom distillery"
				return a
			}()},
		{Name: "titlecase-warning", Desc: "warning prefix in title case (16.22(a)(2) violation)", Expected: "Rejected",
			Fields: GoodFields(), Defects: labelgen.Defects{TitleCaseWarning: true}, App: GoodApp()},
		{Name: "missing-warning", Desc: "government warning absent", Expected: "Rejected",
			Fields: GoodFields(), Defects: labelgen.Defects{OmitWarning: true}, App: GoodApp()},
		{Name: "wrong-brand", Desc: "application asserts a brand not on the label", Expected: "Rejected",
			Fields: GoodFields(), App: func() model.Application {
				a := GoodApp()
				a.BrandName = "STONES THROW BOURBON"
				return a
			}()},
		{Name: "wrong-abv", Desc: "label states 40% ABV, application expects 45%", Expected: "Rejected",
			Fields: func() labelgen.Fields {
				f := GoodFields()
				f.AlcoholContent = "40% Alc./Vol. (80 Proof)"
				return f
			}(), App: GoodApp()},
		{Name: "wrong-net", Desc: "label states 375 mL, application expects 750 mL", Expected: "Rejected",
			Fields: func() labelgen.Fields {
				f := GoodFields()
				f.NetContents = "375 mL"
				return f
			}(), App: GoodApp()},
		{Name: "missing-name", Desc: "no name & address statement on the label", Expected: "Rejected",
			Fields: func() labelgen.Fields {
				f := GoodFields()
				f.NameAddress = ""
				return f
			}(), Defects: labelgen.Defects{OmitNameAddress: true}, App: GoodApp()},
		{Name: "imported-good", Desc: "imported product with country of origin stated", Expected: "Approved",
			Fields: ImportedFields(), App: ImportedApp(true)},
		{Name: "imported-no-origin", Desc: "imported product with no country of origin asserted", Expected: "Needs Correction",
			Fields: ImportedFields(), App: ImportedApp(false)},
	}

	// Conditional-disclosure criteria (27 CFR 5.63(c), 4.32(c-e), 7.63(b)).
	// Each gets a true positive (declared + stated), a true negative (declared
	// but missing), and — where a label can mention the substance without the
	// mandated statement — a red-team negative.
	base := GoodFields()
	baseApp := GoodApp()
	disclose := func(name, desc, expected string, extra []string, set func(*model.Application)) {
		f := base
		f.Extra = extra
		a := baseApp
		set(&a)
		cases = append(cases, Case{Name: name, Desc: desc, Expected: expected, Fields: f, App: a})
	}
	sulfites := func(a *model.Application) { a.ContainsSulfites = true }
	aspartame := func(a *model.Application) { a.ContainsAspartame = true }
	yellow5 := func(a *model.Application) { a.ContainsFDACYellow5 = true }
	carmine := func(a *model.Application) { a.ContainsCarmine = true }
	colored := func(a *model.Application) { a.Colored = true }
	neutral := func(a *model.Application) { a.ContainsNeutralSpirits = true }

	disclose("sulfite-present", "sulfites declared and 'contains sulfites' stated", "Approved", []string{"Contains sulfites"}, sulfites)
	disclose("sulfite-sulfiting-agents", "sulfites declared and 'contains sulfiting agents' stated", "Approved", []string{"Contains sulfiting agents"}, sulfites)
	disclose("sulfite-missing", "sulfites declared but not disclosed", "Rejected", nil, sulfites)
	disclose("sulfite-free-claim", "sulfites declared but label says 'sulfite free' (contradictory)", "Rejected", []string{"Sulfite free"}, sulfites)
	disclose("sulfite-contains-no", "sulfites declared but label says 'contains no sulfites' (negated)", "Rejected", []string{"Contains no sulfites"}, sulfites)

	disclose("aspartame-present", "aspartame declared and all-caps statement present", "Approved", []string{"PHENYLKETONURICS: CONTAINS PHENYLALANINE"}, aspartame)
	disclose("aspartame-titlecase", "aspartame statement in title case (not all caps)", "Rejected", []string{"Phenylketonurics: Contains Phenylalanine"}, aspartame)
	disclose("aspartame-missing", "aspartame declared but statement missing", "Rejected", nil, aspartame)

	disclose("yellow5-present", "FD&C Yellow No. 5 declared and disclosed", "Approved", []string{"Contains FD&C Yellow No. 5"}, yellow5)
	disclose("yellow5-hash-variant", "FD&C Yellow No. 5 disclosed with '#5' variant", "Approved", []string{"Contains FD&C Yellow #5"}, yellow5)
	disclose("yellow5-missing", "FD&C Yellow No. 5 declared but not disclosed", "Rejected", nil, yellow5)
	disclose("yellow5-wrong-number", "label discloses Yellow No. 6, not No. 5", "Rejected", []string{"Contains FD&C Yellow No. 6"}, yellow5)

	disclose("carmine-present", "carmine declared and 'contains carmine' stated", "Approved", []string{"Contains carmine"}, carmine)
	disclose("carmine-cochineal", "carmine declared and 'cochineal extract' stated", "Approved", []string{"Contains cochineal extract"}, carmine)
	disclose("carmine-missing", "carmine declared but not disclosed", "Rejected", nil, carmine)
	disclose("carmine-free-claim", "carmine declared but label says 'carmine free' (contradictory)", "Rejected", []string{"Carmine free"}, carmine)
	disclose("carmine-contains-no", "carmine declared but label says 'contains no carmine' (negated)", "Rejected", []string{"Contains no carmine"}, carmine)

	disclose("coloring-present", "coloring declared and 'colored with' stated", "Approved", []string{"Colored with caramel"}, colored)
	disclose("coloring-treated", "treatment declared and 'treated with' stated", "Approved", []string{"Treated with wood chips"}, colored)
	disclose("coloring-missing", "coloring declared but not disclosed", "Rejected", nil, colored)
	disclose("coloring-color-noun", "label describes 'amber color' without a colorant disclosure", "Rejected", []string{"Color: deep amber"}, colored)

	disclose("neutral-spirits-present", "neutral spirits declared and disclosed", "Approved", []string{"Blended with 20% neutral spirits"}, neutral)
	disclose("neutral-spirits-missing", "neutral spirits declared but not disclosed", "Rejected", nil, neutral)

	// Reverse direction (1.1): a label that discloses a substance/claim the
	// application does NOT declare is a mismatch (the form under-reports), so
	// it defers to a human rather than silently passing.
	reverse := func(name, desc string, extra []string) {
		cases = append(cases, Case{Name: name, Desc: desc, Expected: "Needs Correction",
			Fields: func() labelgen.Fields {
				f := base
				f.Extra = extra
				return f
			}(), App: baseApp})
	}
	reverse("sulfite-reverse", "label discloses sulfites but application does not declare them", []string{"Contains sulfites"})
	reverse("carmine-reverse", "label discloses carmine but application does not declare it", []string{"Contains carmine"})
	reverse("yellow5-reverse", "label discloses Yellow No. 5 but application does not declare it", []string{"Contains FD&C Yellow No. 5"})
	reverse("aspartame-reverse", "label discloses aspartame but application does not declare it", []string{"PHENYLKETONURICS: CONTAINS PHENYLALANINE"})
	reverse("coloring-reverse", "label discloses coloring but application does not declare it", []string{"Colored with caramel"})
	reverse("neutral-reverse", "label discloses neutral spirits but application does not declare them", []string{"Blended with 20% neutral spirits"})
	reverse("age-reverse", "label states an age but application does not assert it", []string{"Aged 12 Years"})
	reverse("distillation-reverse", "label states a distillation state but application does not assert it", []string{"Distilled in Kentucky"})

	// --- age statement (5.63(c)(3)) ------------------------------------------
	ageApp := func(age string) model.Application {
		a := GoodApp()
		a.AgeStatement = age
		return a
	}
	cases = append(cases,
		Case{Name: "age-present", Desc: "age asserted and stated on label", Expected: "Approved",
			Fields: func() labelgen.Fields {
				f := GoodFields()
				f.Extra = []string{"Aged 10 Years"}
				return f
			}(), App: ageApp("Aged 10 Years")},
		Case{Name: "age-missing", Desc: "age asserted but not stated on label", Expected: "Rejected",
			Fields: GoodFields(), App: ageApp("Aged 10 Years")},
		Case{Name: "age-substring", Desc: "age 'Aged 2 Years' must not match 'Aged 22 Years'", Expected: "Rejected",
			Fields: func() labelgen.Fields {
				f := GoodFields()
				f.Extra = []string{"Aged 22 Years"}
				return f
			}(), App: ageApp("Aged 2 Years")},
	)

	// --- state of distillation (5.63(c)(4)) ----------------------------------
	cases = append(cases,
		Case{Name: "distillation-present", Desc: "state of distillation asserted and stated", Expected: "Approved",
			Fields: func() labelgen.Fields {
				f := GoodFields()
				f.Extra = []string{"Distilled in Kentucky"}
				return f
			}(), App: func() model.Application {
				a := GoodApp()
				a.StateOfDistillation = "Kentucky"
				return a
			}()},
		Case{Name: "distillation-missing", Desc: "state of distillation asserted but not on label", Expected: "Rejected",
			Fields: func() labelgen.Fields {
				f := GoodFields()
				f.ClassType = "Bourbon Whiskey" // drop 'Kentucky' from the class type
				return f
			}(), App: func() model.Application {
				a := GoodApp()
				a.ClassType = "Bourbon Whiskey"
				a.StateOfDistillation = "Kentucky"
				return a
			}()},
	)

	// --- malt-beverage alcohol designations (27 CFR 7.65) ---------------------
	cases = append(cases,
		maltDesignation("malt-nonalcoholic-ok", "'non-alcoholic' at 0.4% (compliant)", "Approved", "Non-Alcoholic", "0.4% Alc./Vol.", "0.4%"),
		maltDesignation("malt-nonalcoholic-too-strong", "'non-alcoholic' at 5% (violates <0.5%)", "Rejected", "Non-Alcoholic", "5% Alc./Vol.", "5%"),
		maltDesignation("malt-nonalcoholic-abv-acronym", "'non-alcoholic' at 5% stated as '5% ABV' (non-legal form still violates <0.5%)", "Rejected", "Non-Alcoholic", "5% ABV", "5%"),
		maltDesignation("malt-lowalcohol-ok", "'low alcohol' at 2% (compliant)", "Approved", "Low Alcohol", "2% Alc./Vol.", "2%"),
		maltDesignation("malt-lowalcohol-too-strong", "'low alcohol' at 4% (violates <2.5%)", "Rejected", "Low Alcohol", "4% Alc./Vol.", "4%"),
	)

	return cases
}

// maltDesignation builds a malt-beverage scenario with the given low/non-
// alcoholic designation, ABV statement, and expected ABV.
func maltDesignation(name, desc, expected, designation, abv, expectedABV string) Case {
	return Case{
		Name: name, Desc: desc, Expected: expected,
		Fields: labelgen.Fields{
			BrandName:      "OLD TOM BREWING",
			ClassType:      "Beer",
			AlcoholContent: abv,
			NetContents:    "12 FL OZ",
			NameAddress:    "Bottled by Old Tom Brewing Co., Portland, OR",
			Extra:          []string{designation},
		},
		App: model.Application{
			ProductType:      "malt_beverages",
			Source:           "domestic",
			BrandName:        "OLD TOM BREWING",
			ApplicantName:    "Old Tom Brewing Co.",
			ApplicantAddress: "Portland, OR",
			ClassType:        "Beer",
			AlcoholContent:   expectedABV,
			NetContents:      "12 FL OZ",
		},
	}
}
