package server

import (
	"math"
	"strings"
	"testing"
	"time"

	"Bevcheck/internal/fixtures"
	"Bevcheck/internal/labelgen"
	"Bevcheck/internal/model"
	"Bevcheck/internal/regs"
)

func goodFields() labelgen.Fields {
	return fixtures.GoodFields()
}

func goodApp() *model.Application {
	a := fixtures.GoodApp()
	return &a
}

func eval(f labelgen.Fields, d labelgen.Defects, app *model.Application, minConf float32) (string, []CriterionResult) {
	return evaluate(&verifyContext{app: app, ocr: labelgen.Text(f, d), minConf: minConf})
}

func assertStatus(t *testing.T, results []CriterionResult, name string, want status) {
	t.Helper()
	for _, r := range results {
		if r.Name == name {
			if r.Status != want {
				t.Fatalf("%s status = %s, want %s (%s)", name, r.Status, want, r.Reason)
			}
			return
		}
	}
	t.Fatalf("criterion %q not present in results", name)
}

func TestGoodLabelApproved(t *testing.T) {
	if overall, _ := eval(goodFields(), labelgen.Defects{}, goodApp(), 99); overall != "Approved" {
		t.Fatalf("overall = %s, want Approved", overall)
	}
}

func TestTitleCaseWarningRejected(t *testing.T) {
	overall, results := eval(goodFields(), labelgen.Defects{TitleCaseWarning: true}, goodApp(), 99)
	if overall != "Rejected" {
		t.Fatalf("overall = %s, want Rejected", overall)
	}
	assertStatus(t, results, "government_warning_prefix_caps", statusFail)
}

func TestMissingWarningRejected(t *testing.T) {
	overall, results := eval(goodFields(), labelgen.Defects{OmitWarning: true}, goodApp(), 99)
	if overall != "Rejected" {
		t.Fatalf("overall = %s, want Rejected", overall)
	}
	assertStatus(t, results, "government_warning_present", statusFail)
}

func TestWarningWordingNearMissNeedsCorrection(t *testing.T) {
	// One-edit OCR noise in the otherwise-correct warning must not silently pass.
	f := goodFields()
	f.Warning = strings.Replace(regs.GovernmentWarning, "women", "womem", 1)
	overall, results := eval(f, labelgen.Defects{}, goodApp(), 99)
	if overall != "Needs Correction" {
		t.Fatalf("overall = %s, want Needs Correction", overall)
	}
	assertStatus(t, results, "government_warning_wording", statusNeedsCorrection)
}

func TestWarningFormatHumanConfirm(t *testing.T) {
	// The format rules (bold/type size/contrast) are not machine-verifiable;
	// the criterion surfaces a non-blocking human_confirm flag, so a good label
	// is still Approved while the format gap is made explicit.
	overall, results := eval(goodFields(), labelgen.Defects{}, goodApp(), 99)
	if overall != "Approved" {
		t.Fatalf("overall = %s, want Approved (format flag is non-blocking)", overall)
	}
	assertStatus(t, results, "government_warning_format", statusHumanConfirm)
}

func TestWrongBrandRejected(t *testing.T) {
	app := goodApp()
	app.BrandName = "STONES THROW BOURBON"
	overall, results := eval(goodFields(), labelgen.Defects{}, app, 99)
	if overall != "Rejected" {
		t.Fatalf("overall = %s, want Rejected", overall)
	}
	assertStatus(t, results, "brand_name", statusFail)
}

func TestWrongABVRejected(t *testing.T) {
	app := goodApp()
	app.AlcoholContent = "50%" // 45 vs 50, tolerance 0.3
	overall, results := eval(goodFields(), labelgen.Defects{}, app, 99)
	if overall != "Rejected" {
		t.Fatalf("overall = %s, want Rejected", overall)
	}
	assertStatus(t, results, "alcohol_content", statusFail)
}

func TestLowConfidenceNeedsCorrection(t *testing.T) {
	overall, results := eval(goodFields(), labelgen.Defects{}, goodApp(), 60)
	if overall != "Needs Correction" {
		t.Fatalf("overall = %s, want Needs Correction", overall)
	}
	assertStatus(t, results, "ocr_confidence", statusNeedsCorrection)
}

func TestNetContentsUnitNormalization(t *testing.T) {
	f := goodFields()
	f.NetContents = "750ml" // no space, lowercase — must normalize equal to "750 mL"
	app := goodApp()
	app.NetContents = "750 mL"
	if overall, _ := eval(f, labelgen.Defects{}, app, 99); overall != "Approved" {
		t.Fatalf("overall = %s, want Approved (unit normalization)", overall)
	}
}

func TestNetContentsFlOzVariants(t *testing.T) {
	// "fl oz" must parse even when OCR misreads "l" as "1" ("f1 oz"), and in
	// its common uppercase rendering ("FL OZ").
	cases := []struct {
		ocr  string
		want float64
	}{
		{"12 fl oz", 354.88},
		{"12 FL OZ", 354.88},
		{"12 f1 oz", 354.88},
	}
	for _, c := range cases {
		mL, _, wrongUnit := netContentsVolume("Net contents "+c.ocr, "malt_beverages")
		if wrongUnit {
			t.Fatalf("%q: wrongUnit = true", c.ocr)
		}
		if math.Abs(mL-c.want) > 1 {
			t.Fatalf("%q: mL = %.1f, want ~%.1f", c.ocr, mL, c.want)
		}
	}
}

func TestSpiritsNetContentsMustBeMetric(t *testing.T) {
	f := goodFields()
	f.NetContents = "25.4 fl oz" // U.S. customary alone on a distilled-spirits label
	overall, results := eval(f, labelgen.Defects{}, goodApp(), 99)
	if overall != "Rejected" {
		t.Fatalf("overall = %s, want Rejected (spirits net contents must be metric)", overall)
	}
	assertStatus(t, results, "net_contents", statusFail)
}

func TestStandardFills(t *testing.T) {
	// 700 mL is not a US standard of fill for spirits (5.203).
	f := goodFields()
	f.NetContents = "700 mL"
	app := goodApp()
	app.NetContents = "700 mL"
	overall, results := eval(f, labelgen.Defects{}, app, 99)
	if overall != "Needs Correction" {
		t.Fatalf("overall = %s, want Needs Correction (700 mL non-standard fill)", overall)
	}
	assertStatus(t, results, "net_contents", statusNeedsCorrection)
}

func TestConditionalDisclosures(t *testing.T) {
	cases := []struct {
		name  string
		check checkFunc
		app   *model.Application
		ocr   string
		want  status
	}{
		{"sulfites not declared", checkSulfiteDisclosure, &model.Application{}, "", statusPass},
		{"sulfites disclosed", checkSulfiteDisclosure, &model.Application{ContainsSulfites: true}, "Contains sulfites", statusPass},
		{"sulfites disclosed alt", checkSulfiteDisclosure, &model.Application{ContainsSulfites: true}, "Contains sulfiting agents", statusPass},
		{"sulfites missing", checkSulfiteDisclosure, &model.Application{ContainsSulfites: true}, "no disclosure here", statusFail},

		{"aspartame not declared", checkAspartameDisclosure, &model.Application{}, "", statusPass},
		{"aspartame disclosed", checkAspartameDisclosure, &model.Application{ContainsAspartame: true}, "PHENYLKETONURICS: CONTAINS PHENYLALANINE", statusPass},
		{"aspartame wrong case", checkAspartameDisclosure, &model.Application{ContainsAspartame: true}, "Phenylketonurics: contains phenylalanine", statusFail},
		{"aspartame missing", checkAspartameDisclosure, &model.Application{ContainsAspartame: true}, "no disclosure", statusFail},

		{"yellow5 not declared", checkFDACYellow5Disclosure, &model.Application{}, "", statusPass},
		{"yellow5 disclosed", checkFDACYellow5Disclosure, &model.Application{ContainsFDACYellow5: true}, "Contains FD&C Yellow No. 5", statusPass},
		{"yellow5 missing", checkFDACYellow5Disclosure, &model.Application{ContainsFDACYellow5: true}, "no colorant", statusFail},

		{"carmine not declared", checkCarmineDisclosure, &model.Application{}, "", statusPass},
		{"carmine disclosed", checkCarmineDisclosure, &model.Application{ContainsCarmine: true}, "Contains carmine", statusPass},
		{"cochineal disclosed", checkCarmineDisclosure, &model.Application{ContainsCarmine: true}, "Contains cochineal extract", statusPass},
		{"carmine missing", checkCarmineDisclosure, &model.Application{ContainsCarmine: true}, "no colorant", statusFail},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, _ := c.check(&verifyContext{app: c.app, ocr: c.ocr})
			if s != c.want {
				t.Fatalf("status = %s, want %s", s, c.want)
			}
		})
	}
}

func TestClassTypeVocabulary(t *testing.T) {
	spirits := func(ct string) *model.Application {
		return &model.Application{ProductType: "distilled_spirits", ClassType: ct}
	}
	cases := []struct {
		name string
		app  *model.Application
		ocr  string
		want status
	}{
		{"recognized on label, no assertion", &model.Application{ProductType: "distilled_spirits"}, "Kentucky Straight Bourbon Whiskey 750 mL", statusPass},
		{"asserted and present", spirits("Kentucky Straight Bourbon Whiskey"), "Kentucky Straight Bourbon Whiskey", statusPass},
		{"asserted differs from label", spirits("Vodka"), "Kentucky Straight Bourbon Whiskey", statusFail},
		{"missing from label", &model.Application{ProductType: "distilled_spirits"}, "OLD TOM DISTILLERY 750 mL", statusFail},
		{"asserted non-designation", spirits("Moonshine"), "Moonshine", statusNeedsCorrection},
		{"short term not matched inside word", &model.Application{ProductType: "distilled_spirits"}, "This is an engine part", statusFail},
		{"malt uses malt vocabulary", &model.Application{ProductType: "malt_beverages"}, "India Pale Ale 12 fl oz", statusPass},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, _ := checkClassType(&verifyContext{app: c.app, ocr: c.ocr})
			if s != c.want {
				t.Fatalf("status = %s, want %s", s, c.want)
			}
		})
	}
}

func TestAgeStatement(t *testing.T) {
	app := &model.Application{AgeStatement: "Aged 10 Years"}
	if s, _ := checkAgeStatement(&verifyContext{app: app, ocr: "Kentucky Straight Bourbon Whiskey Aged 10 Years 750 mL"}); s != statusPass {
		t.Fatalf("present age statement should pass")
	}
	if s, _ := checkAgeStatement(&verifyContext{app: app, ocr: "Kentucky Straight Bourbon Whiskey 750 mL"}); s != statusFail {
		t.Fatalf("missing age statement should fail")
	}
	if s, _ := checkAgeStatement(&verifyContext{app: &model.Application{}, ocr: "anything"}); s != statusPass {
		t.Fatalf("unasserted age statement should be not applicable")
	}
}

func TestColoringDisclosure(t *testing.T) {
	app := &model.Application{Colored: true}
	if s, _ := checkColoringDisclosure(&verifyContext{app: app, ocr: "Colored with caramel"}); s != statusPass {
		t.Fatalf("colored disclosure present should pass")
	}
	if s, _ := checkColoringDisclosure(&verifyContext{app: app, ocr: "Treated with wood chips"}); s != statusPass {
		t.Fatalf("treated disclosure present should pass")
	}
	if s, _ := checkColoringDisclosure(&verifyContext{app: app, ocr: "no disclosure"}); s != statusFail {
		t.Fatalf("missing coloring disclosure should fail")
	}
	if s, _ := checkColoringDisclosure(&verifyContext{app: &model.Application{}, ocr: "no disclosure"}); s != statusPass {
		t.Fatalf("unasserted coloring should be not applicable")
	}
}

func TestImportedRequiresImportedBy(t *testing.T) {
	app := &model.Application{Source: "imported", ApplicantName: "Old Tom Import Co.", ApplicantAddress: "Glasgow, Scotland"}
	if s, _ := checkNameAddress(&verifyContext{app: app, ocr: "Bottled by Old Tom Import Co., Glasgow, Scotland"}); s != statusFail {
		t.Fatalf("imported product with 'bottled by' should fail")
	}
	if s, _ := checkNameAddress(&verifyContext{app: app, ocr: "Imported by Old Tom Import Co., Glasgow, Scotland"}); s != statusPass {
		t.Fatalf("imported product with 'imported by' should pass")
	}
	dom := &model.Application{Source: "domestic", ApplicantName: "Old Tom Distillery Co.", ApplicantAddress: "Bardstown, KY"}
	if s, _ := checkNameAddress(&verifyContext{app: dom, ocr: "Bottled by Old Tom Distillery Co., Bardstown, KY"}); s != statusPass {
		t.Fatalf("domestic product with 'bottled by' should pass")
	}
}

func TestNameAddressBrewedBy(t *testing.T) {
	app := &model.Application{Source: "domestic", ApplicantName: "Old Tom Brewing Co.", ApplicantAddress: "Portland, OR"}
	if s, _ := checkNameAddress(&verifyContext{app: app, ocr: "Brewed by Old Tom Brewing Co., Portland, OR"}); s != statusPass {
		t.Fatalf("malt 'brewed by' should satisfy the function phrase, got %s", s)
	}
}

func TestNeutralSpiritsDisclosure(t *testing.T) {
	app := &model.Application{ContainsNeutralSpirits: true}
	if s, _ := checkNeutralSpiritsDisclosure(&verifyContext{app: app, ocr: "Blended with 20% neutral spirits"}); s != statusPass {
		t.Fatalf("neutral spirits disclosure present should pass")
	}
	if s, _ := checkNeutralSpiritsDisclosure(&verifyContext{app: app, ocr: "no disclosure"}); s != statusFail {
		t.Fatalf("missing neutral spirits disclosure should fail")
	}
	if s, _ := checkNeutralSpiritsDisclosure(&verifyContext{app: &model.Application{}, ocr: "no disclosure"}); s != statusPass {
		t.Fatalf("unasserted should be not applicable")
	}
}

func TestStateOfDistillation(t *testing.T) {
	app := &model.Application{StateOfDistillation: "Kentucky"}
	if s, _ := checkStateOfDistillation(&verifyContext{app: app, ocr: "Kentucky Straight Bourbon Whiskey"}); s != statusPass {
		t.Fatalf("state of distillation present should pass")
	}
	if s, _ := checkStateOfDistillation(&verifyContext{app: app, ocr: "Straight Bourbon Whiskey"}); s != statusFail {
		t.Fatalf("missing state of distillation should fail")
	}
	if s, _ := checkStateOfDistillation(&verifyContext{app: &model.Application{}, ocr: "anything"}); s != statusPass {
		t.Fatalf("unasserted should be not applicable")
	}
}

func TestABVAcronymRejected(t *testing.T) {
	f := goodFields()
	f.AlcoholContent = "45% ABV" // colloquial acronym, not a 27 CFR 5.65 form
	_, results := eval(f, labelgen.Defects{}, goodApp(), 99)
	assertStatus(t, results, "alcohol_content", statusNeedsCorrection)
}

func TestMaltAlcoholDesignation(t *testing.T) {
	malt := &model.Application{ProductType: "malt_beverages"}
	if s, _ := checkMaltAlcoholDesignation(&verifyContext{app: malt, ocr: "Non-Alcoholic Beer 0.4% alc/vol"}); s != statusPass {
		t.Fatalf("non-alcoholic with 0.4%% should pass")
	}
	if s, _ := checkMaltAlcoholDesignation(&verifyContext{app: malt, ocr: "Non-Alcoholic Beer 5% alc/vol"}); s != statusFail {
		t.Fatalf("non-alcoholic with 5%% should fail")
	}
	if s, _ := checkMaltAlcoholDesignation(&verifyContext{app: malt, ocr: "Low Alcohol Beer 2% alc/vol"}); s != statusPass {
		t.Fatalf("low alcohol with 2%% should pass")
	}
	if s, _ := checkMaltAlcoholDesignation(&verifyContext{app: malt, ocr: "Low Alcohol Beer 4% alc/vol"}); s != statusFail {
		t.Fatalf("low alcohol with 4%% should fail")
	}
	if s, _ := checkMaltAlcoholDesignation(&verifyContext{app: &model.Application{ProductType: "distilled_spirits"}, ocr: "Non-Alcoholic Beer"}); s != statusPass {
		t.Fatalf("non-malt should be not applicable")
	}
}

func TestProofCrossCheckFlags(t *testing.T) {
	f := goodFields()
	f.AlcoholContent = "45% Alc./Vol. (100 Proof)" // 100 != 90
	overall, results := eval(f, labelgen.Defects{}, goodApp(), 99)
	if overall != "Needs Correction" {
		t.Fatalf("overall = %s, want Needs Correction", overall)
	}
	assertStatus(t, results, "alcohol_content", statusNeedsCorrection)
}

func TestLabelgenRenderRoundTrip(t *testing.T) {
	b, err := labelgen.Render(goodFields(), labelgen.Defects{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(b) == 0 {
		t.Fatalf("empty PNG")
	}
	text := labelgen.Text(goodFields(), labelgen.Defects{})
	if text == "" {
		t.Fatalf("empty ground-truth text")
	}
}

func TestNormalizeFuzzy(t *testing.T) {
	if normalizeFuzzy("STONE'S THROW") != normalizeFuzzy("Stone's Throw") {
		t.Fatalf("case/punct variants should normalize equal")
	}
	if got := normalizeFuzzy("OLD TOM DISTILLERY"); got != "old tom distillery" {
		t.Fatalf("got %q", got)
	}
}

func TestLevenshtein(t *testing.T) {
	if got := levenshtein("kitten", "sitting"); got != 3 {
		t.Fatalf("kitten/sitting = %d, want 3", got)
	}
	if got := levenshtein("", "abc"); got != 3 {
		t.Fatalf("empty/abc = %d, want 3", got)
	}
}

func TestContainsNear(t *testing.T) {
	hay := "GOVERNMENT WARNING: (1) According to the Surgeon General, women should not drink"
	needle := "GOVERNMENT WARNING: (1) According to the Surgeon General, women should not dink"
	if !containsNear(hay, needle, 1) {
		t.Fatalf("one-edit needle should match within distance 1")
	}
}

func TestMashBillNotMisreadAsABV(t *testing.T) {
	// A bourbon mash bill ("60% corn, 36% rye, 4% malted barley") must not be
	// read as alcohol content; the actual "45% Alc./Vol." is the ABV.
	ctx := &verifyContext{
		app: goodApp(),
		ocr: "OLD TOM DISTILLERY Kentucky Straight Bourbon Whiskey " +
			"60% corn 36% rye 4% malted barley 45% Alc./Vol. (90 Proof) 750 mL " +
			"Bottled by Old Tom Distillery Co., Bardstown, KY " + regs.GovernmentWarning,
		minConf: 99,
	}
	overall, results := evaluate(ctx)
	if overall != "Approved" {
		t.Fatalf("overall = %s, want Approved", overall)
	}
	assertStatus(t, results, "alcohol_content", statusPass)
}

func TestDistilledSpiritsMissingABVRejected(t *testing.T) {
	overall, results := eval(goodFields(), labelgen.Defects{OmitAlcohol: true}, goodApp(), 99)
	if overall != "Rejected" {
		t.Fatalf("overall = %s, want Rejected", overall)
	}
	assertStatus(t, results, "alcohol_content", statusFail)
}

func TestWineMissingABVNeedsCorrection(t *testing.T) {
	app := goodApp()
	app.ProductType = "wine"
	app.ClassType = "Cabernet Sauvignon" // varietal, not a table/light designation
	app.AlcoholContent = ""              // nothing asserted; ABV optional for wine <=14%
	f := goodFields()
	f.ClassType = "Cabernet Sauvignon"
	overall, results := eval(f, labelgen.Defects{OmitAlcohol: true}, app, 99)
	if overall != "Needs Correction" {
		t.Fatalf("overall = %s, want Needs Correction", overall)
	}
	assertStatus(t, results, "alcohol_content", statusNeedsCorrection)
}

func TestWineTableWineOmitsABV(t *testing.T) {
	app := goodApp()
	app.ProductType = "wine"
	app.ClassType = "Table Wine"
	app.AlcoholContent = ""
	f := goodFields()
	f.ClassType = "Table Wine"
	overall, results := eval(f, labelgen.Defects{OmitAlcohol: true}, app, 99)
	if overall != "Approved" {
		t.Fatalf("overall = %s, want Approved (table wine may omit ABV)", overall)
	}
	assertStatus(t, results, "alcohol_content", statusPass)
}

func TestMaltMissingABVApproved(t *testing.T) {
	app := goodApp()
	app.ProductType = "malt_beverages"
	app.ClassType = "Beer"
	app.AlcoholContent = "" // nothing asserted; ABV optional for malt
	f := goodFields()
	f.ClassType = "Beer"
	overall, results := eval(f, labelgen.Defects{OmitAlcohol: true}, app, 99)
	if overall != "Approved" {
		t.Fatalf("overall = %s, want Approved", overall)
	}
	assertStatus(t, results, "alcohol_content", statusPass)
}

func TestBrandNameTokenBoundary(t *testing.T) {
	// Brand "TOM" must not match the substring inside "AUTOMATIC".
	app := &model.Application{BrandName: "TOM"}
	ctx := &verifyContext{app: app, ocr: "AUTOMATIC", minConf: 99}
	if st, _ := checkBrandName(ctx); st != statusFail {
		t.Fatalf("brand %q matched substring in %q; want fail, got %s", app.BrandName, ctx.ocr, st)
	}
}

func TestParseABVQualified(t *testing.T) {
	for _, s := range []string{"45%", "45.0", "45% Alc./Vol.", "Alc. 45% by vol", "45 percent"} {
		if v, ok := parseABV(s); !ok || v != 45 {
			t.Fatalf("parseABV(%q) = %v, %v; want 45, true", s, v, ok)
		}
	}
	if _, ok := parseABV("no number here"); ok {
		t.Fatalf("parseABV should reject non-numeric input")
	}
}

func TestUnparseableExpectedABVNeedsCorrection(t *testing.T) {
	app := goodApp()
	app.AlcoholContent = "garbage"
	ctx := &verifyContext{app: app, ocr: labelgen.Text(goodFields(), labelgen.Defects{}), minConf: 99}
	if st, _ := checkAlcoholContent(ctx); st != statusNeedsCorrection {
		t.Fatalf("unparseable expected ABV should be needs_correction, got %s", st)
	}
}

func TestUSCustomaryVolumeUnits(t *testing.T) {
	cases := []struct {
		s    string
		want float64
	}{
		{"750 mL", 750},
		{"1 L", 1000},
		{"1 pint", 473.176473},
		{"1 pt", 473.176473},
		{"1 quart", 946.352946},
		{"1 qt", 946.352946},
		{"1 gallon", 3785.411784},
		{"1 gal", 3785.411784},
		{"25.4 fl oz", 25.4 * 29.5735},
	}
	for _, c := range cases {
		v, ok := parseVolume(c.s)
		if !ok || math.Abs(v-c.want) > 0.001 {
			t.Fatalf("parseVolume(%q) = %v, %v; want ~%v", c.s, v, ok, c.want)
		}
	}
}

func TestNameAddressChecksAddress(t *testing.T) {
	ocr := labelgen.Text(goodFields(), labelgen.Defects{})

	app := goodApp()
	app.ApplicantAddress = "Lexington, KY" // not on the label
	if st, _ := checkNameAddress(&verifyContext{app: app, ocr: ocr, minConf: 99}); st != statusFail {
		t.Fatalf("address mismatch should fail, got %s", st)
	}

	app = goodApp()
	app.ApplicantAddress = ""
	if st, _ := checkNameAddress(&verifyContext{app: app, ocr: ocr, minConf: 99}); st != statusNeedsCorrection {
		t.Fatalf("missing address in app should be needs_correction, got %s", st)
	}
}

func TestValidateApplication(t *testing.T) {
	for _, pt := range []string{"wine", "distilled_spirits", "malt_beverages"} {
		if err := validateApplication(&model.Application{ProductType: pt, Source: "domestic"}); err != nil {
			t.Fatalf("product_type %q should be valid: %v", pt, err)
		}
	}
	if err := validateApplication(&model.Application{ProductType: "beer", Source: "domestic"}); err == nil {
		t.Fatalf("product_type beer should be rejected")
	}
	if err := validateApplication(&model.Application{ProductType: "wine", Source: "exported"}); err == nil {
		t.Fatalf("source exported should be rejected")
	}
}

func TestDailyLimiter(t *testing.T) {
	l := newDailyLimiter(2000)
	if !l.allow(300) {
		t.Fatalf("300 docs should be allowed")
	}
	if !l.allow(1700) {
		t.Fatalf("1700 docs should be allowed (total 2000)")
	}
	if l.allow(1) {
		t.Fatalf("should exceed daily limit")
	}
	// simulate the next UTC day: counter must reset
	l.mu.Lock()
	l.day = time.Now().UTC().Unix()/86400 - 1
	l.count = 2000
	l.mu.Unlock()
	if !l.allow(1) {
		t.Fatalf("new day should reset the counter")
	}
}

// TestDisclosureEdgeCases red-teams the conditional-disclosure and designation
// heuristics with OCR variants: British spellings, OCR-mangled ampersands,
// contradictory "free"/"no ..." claims, and substring-boundary traps.
func TestDisclosureEdgeCases(t *testing.T) {
	cases := []struct {
		name  string
		check checkFunc
		app   *model.Application
		ocr   string
		want  status
	}{
		// Sulfites: a positive "contains" disclosure only.
		{"sulfites british spelling", checkSulfiteDisclosure, &model.Application{ContainsSulfites: true}, "Contains sulphites", statusPass},
		{"sulfites paren agent", checkSulfiteDisclosure, &model.Application{ContainsSulfites: true}, "Contains (a) sulfiting agent(s)", statusPass},
		{"sulfites free claim", checkSulfiteDisclosure, &model.Application{ContainsSulfites: true}, "Sulfite free", statusFail},
		{"sulfites no added", checkSulfiteDisclosure, &model.Application{ContainsSulfites: true}, "No sulfites added", statusFail},
		{"sulfites bare word", checkSulfiteDisclosure, &model.Application{ContainsSulfites: true}, "sulfites", statusFail},

		// Carmine/cochineal: a positive "contains" disclosure only.
		{"carmine paren cochineal", checkCarmineDisclosure, &model.Application{ContainsCarmine: true}, "Contains cochineal extract (carmine)", statusPass},
		{"carmine free claim", checkCarmineDisclosure, &model.Application{ContainsCarmine: true}, "Carmine free", statusFail},
		{"carmine no added", checkCarmineDisclosure, &model.Application{ContainsCarmine: true}, "No carmine added", statusFail},

		// FD&C Yellow No. 5: tolerant of OCR-mangled forms, strict on the number.
		{"yellow5 no dot", checkFDACYellow5Disclosure, &model.Application{ContainsFDACYellow5: true}, "Contains FD&C Yellow No 5", statusPass},
		{"yellow5 bare", checkFDACYellow5Disclosure, &model.Application{ContainsFDACYellow5: true}, "Contains FD&C Yellow 5", statusPass},
		{"yellow5 hash spaced", checkFDACYellow5Disclosure, &model.Application{ContainsFDACYellow5: true}, "Contains FD&C Yellow # 5", statusPass},
		{"yellow5 wrong number", checkFDACYellow5Disclosure, &model.Application{ContainsFDACYellow5: true}, "Contains FD&C Yellow No. 6", statusFail},

		// Coloring: a treatment/coloring action, not a bare color noun.
		{"coloring artificially colored", checkColoringDisclosure, &model.Application{Colored: true}, "Artificially colored with caramel", statusPass},
		{"coloring caramel coloring", checkColoringDisclosure, &model.Application{Colored: true}, "Caramel coloring added", statusPass},
		{"coloring british", checkColoringDisclosure, &model.Application{Colored: true}, "Coloured with caramel", statusPass},
		{"coloring bare color noun", checkColoringDisclosure, &model.Application{Colored: true}, "Color: deep amber", statusFail},

		// Age statement: word-boundary aware so "10 Years" != "110 Years".
		{"age longer phrase", checkAgeStatement, &model.Application{AgeStatement: "Aged 10 Years"}, "Aged 10 Years Old", statusPass},
		{"age substring 110", checkAgeStatement, &model.Application{AgeStatement: "Aged 10 Years"}, "Aged 110 Years", statusFail},
		{"age substring 22", checkAgeStatement, &model.Application{AgeStatement: "Aged 2 Years"}, "Aged 22 Years", statusFail},

		// State of distillation: word-boundary aware, multi-word states.
		{"state multi word", checkStateOfDistillation, &model.Application{StateOfDistillation: "New York"}, "New York Straight Rye Whiskey", statusPass},
		{"state substring", checkStateOfDistillation, &model.Application{StateOfDistillation: "New York"}, "New Yorker Whiskey", statusFail},

		// Neutral spirits: singular and plural both acceptable.
		{"neutral spirit singular", checkNeutralSpiritsDisclosure, &model.Application{ContainsNeutralSpirits: true}, "20% neutral spirit distilled from grain", statusPass},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, _ := c.check(&verifyContext{app: c.app, ocr: c.ocr})
			if s != c.want {
				t.Fatalf("status = %s, want %s", s, c.want)
			}
		})
	}
}

func TestRunCheckRecoversPanic(t *testing.T) {
	r := runCheck("boom", func(ctx *verifyContext) (status, string) {
		panic("kaboom")
	}, &verifyContext{})
	if r.Name != "boom" {
		t.Fatalf("name = %q, want boom", r.Name)
	}
	if r.Status != statusNeedsCorrection {
		t.Fatalf("status = %s, want %s", r.Status, statusNeedsCorrection)
	}
	if r.Reason == "" {
		t.Fatalf("reason should be non-empty")
	}
}

func TestDisclosureFound(t *testing.T) {
	cases := []struct {
		name string
		ocr  string
		sub  string
		want bool
	}{
		{"sulfites affirmative", "Contains sulfites", `sul(?:f|ph)it`, true},
		{"sulfites agent", "Contains a sulfiting agent", `sul(?:f|ph)it`, true},
		{"sulfites british", "contains sulphites", `sul(?:f|ph)it`, true},
		{"sulfites free", "Sulfite free", `sul(?:f|ph)it`, false},
		{"sulfites no", "no sulfites", `sul(?:f|ph)it`, false},
		{"sulfites contains no", "contains no sulfites", `sul(?:f|ph)it`, false},
		{"sulfites does not contain", "does not contain sulfites", `sul(?:f|ph)it`, false},
		{"carmine affirmative", "Contains carmine", `(?:carmine|cochineal)`, true},
		{"carmine cochineal", "Contains cochineal extract", `(?:carmine|cochineal)`, true},
		{"carmine contains no", "contains no carmine", `(?:carmine|cochineal)`, false},
		{"carmine does not contain", "does not contain carmine", `(?:carmine|cochineal)`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := disclosureFound(c.ocr, c.sub); got != c.want {
				t.Fatalf("disclosureFound(%q, %q) = %v, want %v", c.ocr, c.sub, got, c.want)
			}
		})
	}
}

func TestReverseDisclosureNeedsCorrection(t *testing.T) {
	// The label discloses sulfites but the application does not declare them:
	// the reverse direction must flag the mismatch, not silently pass.
	f := goodFields()
	f.Extra = []string{"Contains sulfites"}
	overall, results := eval(f, labelgen.Defects{}, goodApp(), 99)
	if overall != "Needs Correction" {
		t.Fatalf("overall = %s, want Needs Correction", overall)
	}
	assertStatus(t, results, "sulfite_disclosure", statusNeedsCorrection)
}

func TestFindABVAcronym(t *testing.T) {
	cases := []struct {
		s    string
		want float64
		ok   bool
	}{
		{"5% ABV", 5, true},
		{"0.4% ABV", 0.4, true},
		{"45% alc/vol", 0, false}, // legal form, not the acronym
		{"Non-Alcoholic 5% ABV", 5, true},
		{"ABV", 0, false},
	}
	for _, c := range cases {
		v, ok := findABVAcronym(c.s)
		if ok != c.ok || (ok && v != c.want) {
			t.Fatalf("findABVAcronym(%q) = (%v, %v), want (%v, %v)", c.s, v, ok, c.want, c.ok)
		}
	}
}

func TestImportedFunctionPhrase(t *testing.T) {
	if !reImportedBy.MatchString("Imported by Old Tom Import Co.") {
		t.Fatalf("'Imported by' should match")
	}
	if !reImportedBy.MatchString("Imported and bottled by Old Tom Import Co.") {
		t.Fatalf("'Imported and bottled by' should match")
	}
	if reImportedBy.MatchString("Imported and sold by Old Tom Import Co.") {
		t.Fatalf("'Imported and sold by' should NOT match")
	}
}

func TestCountryOfOriginWordBoundary(t *testing.T) {
	// A country substring must not false-match inside a longer token.
	ctx := &verifyContext{
		app: &model.Application{Source: "imported", CountryOfOrigin: "Peru"},
		ocr: "Imported by Andes Imports, Peruvian brandy",
	}
	if s, _ := checkCountryOfOrigin(ctx); s != statusFail {
		t.Fatalf("'Peru' must not match 'Peruvian': got %s", s)
	}
	ctx.ocr = "Imported by Andes Imports, Product of Peru"
	if s, _ := checkCountryOfOrigin(ctx); s != statusPass {
		t.Fatalf("'Product of Peru' should match: got %s", s)
	}
}

func TestMaltDesignationBarePct(t *testing.T) {
	ctx := &verifyContext{app: &model.Application{ProductType: "malt_beverages"}}
	cases := []struct {
		ocr  string
		want status
	}{
		{"Non-Alcoholic 5%", statusFail},   // >= 0.5, bare %
		{"Non-Alcoholic 0.4%", statusPass}, // < 0.5, bare %
		{"Low Alcohol 3%", statusFail},     // >= 2.5, bare %
		{"Low Alcohol 2%", statusPass},     // < 2.5, bare %
	}
	for _, c := range cases {
		ctx.ocr = c.ocr
		if s, _ := checkMaltAlcoholDesignation(ctx); s != c.want {
			t.Fatalf("checkMaltAlcoholDesignation(%q) = %s, want %s", c.ocr, s, c.want)
		}
	}
}
