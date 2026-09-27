package server

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"Bevcheck/internal/model"
	"Bevcheck/internal/regs"
)

// status is a single criterion's resolution.
type status string

const (
	statusPass            status = "pass"
	statusFail            status = "fail"
	statusNeedsCorrection status = "needs_correction"
	// statusHumanConfirm is a non-blocking flag: the criterion cannot be
	// machine-verified and needs a human to confirm, but it does not change
	// the overall pass/fail verdict.
	statusHumanConfirm status = "human_confirm"
)

// minConfidenceThreshold flags the whole label for human review when the least
// confident OCR line drops below this (research.txt 7.8.6).
const minConfidenceThreshold = 85

// Application now lives in internal/model (the API contract shared with the
// fixture generator). See model.Application for the schema.

type CriterionResult struct {
	Name   string `json:"criterion"`
	Status status `json:"status"`
	Reason string `json:"reason"`
}

type verifyContext struct {
	app     *model.Application
	ocr     string // whitespace-collapsed OCR text
	minConf float32
}

type checkFunc func(ctx *verifyContext) (status, string)

type criterion struct {
	name  string
	check checkFunc
}

// criteria is the conjunctive set (research.txt 7.6.9): the label is Approved
// iff every criterion passes. Deterministic fuzzy-string checks only.
var criteria = []criterion{
	{"ocr_confidence", checkOCRConfidence},
	{"brand_name", checkBrandName},
	{"class_type", checkClassType},
	{"alcohol_content", checkAlcoholContent},
	{"net_contents", checkNetContents},
	{"name_address", checkNameAddress},
	{"country_of_origin", checkCountryOfOrigin},
	{"government_warning_present", checkWarningPresent},
	{"government_warning_wording", checkWarningWording},
	{"government_warning_prefix_caps", checkWarningCaps},
	{"government_warning_format", checkWarningFormat},
	{"sulfite_disclosure", checkSulfiteDisclosure},
	{"aspartame_disclosure", checkAspartameDisclosure},
	{"fdc_yellow5_disclosure", checkFDACYellow5Disclosure},
	{"carmine_disclosure", checkCarmineDisclosure},
	{"age_statement", checkAgeStatement},
	{"coloring_disclosure", checkColoringDisclosure},
	{"neutral_spirits_disclosure", checkNeutralSpiritsDisclosure},
	{"state_of_distillation", checkStateOfDistillation},
	{"malt_alcohol_designation", checkMaltAlcoholDesignation},
}

// evaluate runs every criterion in parallel and folds them into a conjunctive
// tri-state verdict: any hard fail -> Rejected; else any needs_correction ->
// Needs Correction; else Approved (research.txt 7.7).
func evaluate(ctx *verifyContext) (string, []CriterionResult) {
	results := make([]CriterionResult, len(criteria))
	var wg sync.WaitGroup
	for i, c := range criteria {
		wg.Add(1)
		go func(i int, name string, check checkFunc) {
			defer wg.Done()
			results[i] = runCheck(name, check, ctx)
		}(i, c.name, c.check)
	}
	wg.Wait()

	// statusHumanConfirm is deliberately non-blocking: it flags a criterion a
	// human must confirm (e.g. warning format) without changing the verdict.
	needsCorrection := false
	for _, r := range results {
		switch r.Status {
		case statusFail:
			return "Rejected", results
		case statusNeedsCorrection:
			needsCorrection = true
		}
	}
	if needsCorrection {
		return "Needs Correction", results
	}
	return "Approved", results
}

// runCheck executes one criterion, converting a panic into a needs_correction
// result so a single faulty check cannot crash the whole process.
func runCheck(name string, check checkFunc, ctx *verifyContext) (r CriterionResult) {
	defer func() {
		if rec := recover(); rec != nil {
			r = CriterionResult{Name: name, Status: statusNeedsCorrection, Reason: fmt.Sprintf("criterion %s panicked: %v", name, rec)}
		}
	}()
	st, reason := check(ctx)
	return CriterionResult{Name: name, Status: st, Reason: reason}
}

func minLineConfidence(lines []OCRLine) float32 {
	if len(lines) == 0 {
		return 0
	}
	m := lines[0].Confidence
	for _, l := range lines[1:] {
		if l.Confidence < m {
			m = l.Confidence
		}
	}
	return m
}

// ---------------------------------------------------------------------------
// normalization (research.txt 8.2, 7.6.1)
// ---------------------------------------------------------------------------

var (
	reWhitespace = regexp.MustCompile(`\s+`)
	rePunct      = regexp.MustCompile(`[^\pL\pN]+`)
)

// collapseWS collapses all whitespace to single spaces, preserving case and
// punctuation (the text fed to the criteria).
func collapseWS(s string) string {
	return strings.TrimSpace(reWhitespace.ReplaceAllString(s, " "))
}

// normalizeWording lower-cases and collapses whitespace, keeping punctuation
// and word order (warning wording comparison).
func normalizeWording(s string) string {
	return collapseWS(strings.ToLower(s))
}

// normalizeFuzzy lower-cases, collapses whitespace, and strips punctuation.
// Mechanical, not model-based.
func normalizeFuzzy(s string) string {
	return collapseWS(rePunct.ReplaceAllString(strings.ToLower(s), " "))
}

// containsPhrase reports whether phrase appears in the normalized text as a
// whole word/phrase (word-boundary aware, so "TOM" does not match "AUTOMATIC").
func containsPhrase(norm, phrase string) bool {
	return strings.Contains(" "+norm+" ", " "+phrase+" ")
}

// ---------------------------------------------------------------------------
// criteria (research.txt 7.6)
// ---------------------------------------------------------------------------

func checkOCRConfidence(ctx *verifyContext) (status, string) {
	if ctx.ocr == "" {
		return statusFail, "no text extracted"
	}
	if ctx.minConf < minConfidenceThreshold {
		return statusNeedsCorrection, fmt.Sprintf("low OCR confidence (min %.0f%%)", ctx.minConf)
	}
	return statusPass, fmt.Sprintf("OCR confidence sufficient (min %.0f%%)", ctx.minConf)
}

func checkBrandName(ctx *verifyContext) (status, string) {
	brand := strings.TrimSpace(ctx.app.BrandName)
	if brand == "" {
		return statusNeedsCorrection, "no brand name asserted in application"
	}
	nb := normalizeFuzzy(brand)
	if nb == "" {
		return statusNeedsCorrection, "brand name is empty after normalization"
	}
	if containsPhrase(normalizeFuzzy(ctx.ocr), nb) {
		return statusPass, fmt.Sprintf("brand name %q found on label", brand)
	}
	return statusFail, fmt.Sprintf("brand name %q not found on label", brand)
}

func checkClassType(ctx *verifyContext) (status, string) {
	vocab := classVocabulary(ctx.app.ProductType)
	want := strings.TrimSpace(ctx.app.ClassType)

	if found, ok := classTypeFound(ctx.ocr, vocab); ok {
		// A recognized designation is on the label.
		if want != "" && !strings.Contains(normalizeFuzzy(ctx.ocr), normalizeFuzzy(want)) {
			return statusFail, fmt.Sprintf("class/type %q not found on label (found %q)", want, found)
		}
		return statusPass, fmt.Sprintf("class/type %q found", found)
	}

	// No recognized designation on the label. Distinguish "asserted but not a
	// recognized standard of identity" from a plainly missing class/type.
	if want != "" && strings.Contains(normalizeFuzzy(ctx.ocr), normalizeFuzzy(want)) {
		return statusNeedsCorrection, fmt.Sprintf("class/type %q is not a recognized standard of identity", want)
	}
	return statusFail, "class/type designation not found on label (mandatory, 5.63(a)(2)/4.32/7.63)"
}

// classVocabulary returns the controlled vocabulary for the product type.
func classVocabulary(productType string) []string {
	switch productType {
	case "wine":
		return regs.WineClasses
	case "malt_beverages":
		return regs.MaltBeverageClasses
	default: // distilled_spirits, unknown
		return regs.DistilledSpiritsClasses
	}
}

// classTypeFound reports whether any vocabulary term appears in ocr as a whole
// phrase (word-boundary aware), returning the longest (most specific) match.
func classTypeFound(ocr string, vocab []string) (string, bool) {
	norm := normalizeFuzzy(ocr)
	best := ""
	for _, term := range vocab {
		if containsPhrase(norm, term) && len(term) > len(best) {
			best = term
		}
	}
	return best, best != ""
}

var (
	reProof = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*proof`)

	// A "%" (or "percent") counts as alcohol content only when an alcohol
	// qualifier (alc/alcohol/vol/volume) is nearby, so a mash bill
	// ("60% corn, 36% rye") is not misread as ABV. "abv" is deliberately
	// excluded: it is not a 27 CFR 5.65 form (see reABVAcronym).
	rePctABV        = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*%`)
	rePctQualifier  = regexp.MustCompile(`(?i)\b(?:alc|alcohol|vol|volume)\b`)
	reABVWordAfter  = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*(?:percent|pct)\s+(?:alcohol|alc)\b`)
	reABVWordBefore = regexp.MustCompile(`(?i)\b(?:alcohol|alc)\b[^0-9%]{0,20}(\d+(?:\.\d+)?)\s*(?:percent|pct)\b`)

	// reABVAcronym matches "45% ABV" — the colloquial acronym, not a legal
	// 5.65 alcohol-content form.
	reABVAcronym = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*%\s*abv\b`)

	// reBareNumber matches a leading number; lenient fallback for an expected
	// alcohol content like "45" or "45%".
	reBareNumber = regexp.MustCompile(`(\d+(?:\.\d+)?)`)

	// reTableLightWine matches the "table wine"/"light wine" designations that
	// imply <=14% ABV, making an omitted alcohol-content statement legal (4.36).
	reTableLightWine = regexp.MustCompile(`(?i)\b(table|light)\s+wine\b`)

	// Malt-beverage alcohol designations (27 CFR 7.65): "non-alcoholic"/"alcohol
	// free" require <0.5% ABV, "low alcohol" requires <2.5% ABV.
	reNonAlcoholic = regexp.MustCompile(`(?i)\bnon-alcoholic\b|\balcohol\s+free\b`)
	reLowAlcohol   = regexp.MustCompile(`(?i)\blow\s+alcohol\b`)
)

// findABV returns the alcohol-by-volume percentage in the OCR text. It only
// accepts a "%" (or "percent") that is part of an alcohol statement, so a mash
// bill ("60% corn, 36% rye") or other percentage is not misread as ABV.
func findABV(s string) (float64, bool) {
	if m := reABVWordAfter.FindStringSubmatch(s); m != nil {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			return v, true
		}
	}
	if m := reABVWordBefore.FindStringSubmatch(s); m != nil {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			return v, true
		}
	}
	// The qualifier window is intentionally narrow (12 chars) so a "%" in one
	// statement cannot borrow the "alc/vol" of an adjacent statement (e.g. a
	// mash bill "%" sitting before the real ABV).
	for _, m := range rePctABV.FindAllStringSubmatchIndex(s, -1) {
		lo := m[0] - 12
		if lo < 0 {
			lo = 0
		}
		hi := m[1] + 12
		if hi > len(s) {
			hi = len(s)
		}
		if rePctQualifier.MatchString(s[lo:hi]) {
			if v, err := strconv.ParseFloat(s[m[2]:m[3]], 64); err == nil {
				return v, true
			}
		}
	}
	return 0, false
}

// findABVAcronym returns the numeric value of an "N% ABV" statement. The
// acronym is not a legal 27 CFR 5.65 form (checkAlcoholContent flags it), but
// the value still constrains the malt-beverage low/non-alcoholic thresholds
// (7.65), which would otherwise silently skip it.
func findABVAcronym(s string) (float64, bool) {
	if m := reABVAcronym.FindStringSubmatch(s); m != nil {
		v, err := strconv.ParseFloat(m[1], 64)
		return v, err == nil
	}
	return 0, false
}

// findABVBarePct returns the first bare "N%" with no alcohol qualifier, used
// only by the malt low/non-alcoholic threshold check, where a bare percentage
// is the stated ABV. A bare percentage is otherwise excluded to avoid
// misreading a mash bill ("60% corn, 36% rye").
func findABVBarePct(s string) (float64, bool) {
	if m := rePctABV.FindStringSubmatch(s); m != nil {
		v, err := strconv.ParseFloat(m[1], 64)
		return v, err == nil
	}
	return 0, false
}

func checkAlcoholContent(ctx *verifyContext) (status, string) {
	abv, found := findABV(ctx.ocr)
	if !found {
		if reABVAcronym.MatchString(ctx.ocr) {
			return statusNeedsCorrection, `alcohol content stated as "ABV"; use "alc/vol" or "alcohol by volume" (27 CFR 5.65)`
		}
		// No alcohol statement on the label. Whether that is a failure depends
		// on the beverage type: distilled spirits must state ABV (5.65), but
		// wine (4.36) and malt beverages (7.65) may omit it.
		if strings.TrimSpace(ctx.app.AlcoholContent) != "" {
			return statusFail, "alcohol content asserted but not found on label"
		}
		switch ctx.app.ProductType {
		case "wine":
			// 4.36: ABV optional at <=14%, required above 14%. A "table wine" or
			// "light wine" designation implies <=14%, so an omitted statement is
			// legal; otherwise defer to a human.
			if reTableLightWine.MatchString(ctx.ocr) {
				return statusPass, "alcohol content omitted; 'table wine'/'light wine' designation present (<=14% ABV, 27 CFR 4.36)"
			}
			return statusNeedsCorrection, "alcohol content not stated; optional for wine <=14% ABV (27 CFR 4.36), required above 14% -- human confirm"
		case "malt_beverages":
			// 7.65: optional unless the beverage contains alcohol from added
			// nonbeverage flavors (a formula question, out of scope).
			return statusPass, "alcohol content not stated; optional for malt beverages (27 CFR 7.65)"
		default: // distilled_spirits, unknown
			return statusFail, "alcohol content not found (mandatory, 27 CFR 5.65)"
		}
	}

	reason := fmt.Sprintf("alcohol content found: %.1f%%", abv)

	// proof cross-check: proof must be 2x ABV (27 CFR 5.65(b)(1)).
	if p := reProof.FindStringSubmatch(ctx.ocr); p != nil {
		if proof, err := strconv.ParseFloat(p[1], 64); err == nil && math.Abs(proof-2*abv) > 0.6 {
			return statusNeedsCorrection, fmt.Sprintf("%s; proof %.0f inconsistent with 2x ABV", reason, proof)
		}
	}

	if strings.TrimSpace(ctx.app.AlcoholContent) != "" {
		exp, ok := parseABV(ctx.app.AlcoholContent)
		if !ok {
			return statusNeedsCorrection, fmt.Sprintf("could not parse expected alcohol content %q", ctx.app.AlcoholContent)
		}
		if math.Abs(abv-exp) > regs.AlcoholTolerance(ctx.app.ProductType, abv) {
			return statusFail, fmt.Sprintf("alcohol content %.1f%% out of tolerance vs expected %.1f%%", abv, exp)
		}
	}
	return statusPass, reason
}

func parseABV(s string) (float64, bool) {
	if v, ok := findABV(s); ok {
		return v, true
	}
	// Fallback: a bare number like "45" or "45%".
	if m := reBareNumber.FindStringSubmatch(strings.TrimSpace(s)); m != nil {
		v, err := strconv.ParseFloat(m[1], 64)
		return v, err == nil
	}
	return 0, false
}

var (
	// reMetricVolume matches a metric net-contents statement (mL or L).
	reMetricVolume = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*(ml\b|milliliters?|millilitres?|liters?|litres?|\bl\b)`)
	// reUSVolume matches a U.S. customary net-contents statement. The fluid-
	// ounce unit accepts "f1" for "fl": OCR commonly misreads the lowercase
	// "l" as the digit "1".
	reUSVolume = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*(f[l1]\.?\s*oz\.?|pints?|\bpt\b|quarts?|\bqt\b|gallons?|\bgal\b|\boz\.?)`)
)

// netContentsToleranceFraction is the allowed relative net-contents deviation
// (1%) when comparing the label's stated volume to the application's expected
// value; it absorbs unit-conversion rounding, not a real short-fill.
const netContentsToleranceFraction = 0.01

func checkNetContents(ctx *verifyContext) (status, string) {
	mL, token, wrongUnit := netContentsVolume(ctx.ocr, ctx.app.ProductType)
	if wrongUnit {
		return statusFail, "net contents must be stated in metric units (27 CFR 5.70/4.37)"
	}
	if token == "" {
		return statusFail, "net contents not found"
	}
	reason := fmt.Sprintf("net contents found: %s (%.0f mL)", token, mL)
	if strings.TrimSpace(ctx.app.NetContents) != "" {
		exp, ok := parseVolume(ctx.app.NetContents)
		if !ok {
			return statusNeedsCorrection, fmt.Sprintf("could not parse expected net contents %q", ctx.app.NetContents)
		}
		if math.Abs(mL-exp) > netContentsToleranceFraction*exp {
			return statusFail, fmt.Sprintf("net contents %.0f mL vs expected %.0f mL", mL, exp)
		}
	}
	if fills, ok := standardFillsFor(ctx.app.ProductType); ok && !isStandardFill(mL, fills) {
		return statusNeedsCorrection, fmt.Sprintf("net contents %.0f mL is not an authorized standard of fill (27 CFR 5.203/4.72)", mL)
	}
	return statusPass, reason
}

// netContentsVolume extracts the net contents from OCR as canonical mL,
// honoring the unit system each product type requires (research.txt 2.4.4):
// distilled spirits (5.70) and wine (4.37) must state metric; malt beverages
// (7.70) use U.S. customary but metric is also accepted. wrongUnit is set when
// a volume appears only in the disallowed system.
func netContentsVolume(ocr, productType string) (mL float64, token string, wrongUnit bool) {
	if m := reMetricVolume.FindStringSubmatch(ocr); m != nil {
		return toMilliliters(m[1], m[2]), m[0], false
	}
	if m := reUSVolume.FindStringSubmatch(ocr); m != nil {
		if productType == "malt_beverages" {
			return toMilliliters(m[1], m[2]), m[0], false
		}
		return 0, "", true
	}
	return 0, "", false
}

// Authorized container sizes in mL. 27 CFR 5.203 (distilled spirits) and 4.72
// (wine). Malt beverages have no fixed standards of fill (7.70). A non-standard
// size is surfaced for review (needs_correction), not hard-failed, because
// formula-approved exceptions exist and the wine list omits large formats
// (>3 L).
var (
	spiritsStandardFills = []float64{50, 100, 200, 375, 500, 750, 1000, 1750}
	wineStandardFills    = []float64{50, 100, 187, 375, 500, 750, 1000, 1500, 3000}
)

func standardFillsFor(productType string) ([]float64, bool) {
	switch productType {
	case "distilled_spirits":
		return spiritsStandardFills, true
	case "wine":
		return wineStandardFills, true
	default: // malt_beverages, unknown
		return nil, false
	}
}

func isStandardFill(mL float64, fills []float64) bool {
	for _, f := range fills {
		if math.Abs(mL-f) < 0.5 {
			return true
		}
	}
	return false
}

func toMilliliters(num, unit string) float64 {
	v, _ := strconv.ParseFloat(num, 64)
	u := strings.ToLower(strings.TrimSpace(unit))
	switch {
	case u == "l" || strings.HasPrefix(u, "lit"):
		return v * 1000
	case strings.Contains(u, "gal"):
		return v * 3785.411784
	case strings.Contains(u, "qt") || strings.Contains(u, "quart"):
		return v * 946.352946
	case strings.Contains(u, "pt") || strings.Contains(u, "pint"):
		return v * 473.176473
	case strings.Contains(u, "oz"):
		return v * 29.5735
	default:
		return v // mL, milliliter(s), millilitre(s)
	}
}

func parseVolume(s string) (float64, bool) {
	if m := reMetricVolume.FindStringSubmatch(s); m != nil {
		return toMilliliters(m[1], m[2]), true
	}
	if m := reUSVolume.FindStringSubmatch(s); m != nil {
		return toMilliliters(m[1], m[2]), true
	}
	return 0, false
}

var (
	reFunctionPhrase = regexp.MustCompile(`(?i)\b(bottled|distilled|produced|made|blended|vinted|cellared|packed|brewed)\s+by\b`)
	reImportedBy     = regexp.MustCompile(`(?i)\bimported(?:\s+and\s+(?:bottled|packed))?\s+by\b`)
)

func checkNameAddress(ctx *verifyContext) (status, string) {
	src := strings.ToLower(strings.TrimSpace(ctx.app.Source))
	if src == "imported" {
		if !reImportedBy.MatchString(ctx.ocr) {
			return statusFail, "imported product must state 'imported by' or 'imported and bottled by' (27 CFR 5.67/5.68)"
		}
	} else if !reFunctionPhrase.MatchString(ctx.ocr) {
		return statusFail, "name/address function phrase (e.g. 'bottled by') not found"
	}
	name := strings.TrimSpace(ctx.app.ApplicantName)
	if name == "" {
		return statusNeedsCorrection, "applicant name not asserted in application"
	}
	if !strings.Contains(normalizeFuzzy(ctx.ocr), normalizeFuzzy(name)) {
		return statusFail, fmt.Sprintf("applicant name %q not found on label", name)
	}
	addr := strings.TrimSpace(ctx.app.ApplicantAddress)
	if addr == "" {
		return statusNeedsCorrection, "applicant address not asserted in application"
	}
	if !strings.Contains(normalizeFuzzy(ctx.ocr), normalizeFuzzy(addr)) {
		return statusFail, fmt.Sprintf("applicant address %q not found on label", addr)
	}
	return statusPass, "name and address statement present"
}

func checkCountryOfOrigin(ctx *verifyContext) (status, string) {
	src := strings.ToLower(strings.TrimSpace(ctx.app.Source))
	if src == "" || src == "domestic" {
		return statusPass, "not applicable (domestic product)"
	}
	if strings.TrimSpace(ctx.app.CountryOfOrigin) != "" {
		want := ctx.app.CountryOfOrigin
		if containsPhrase(normalizeFuzzy(ctx.ocr), normalizeFuzzy(want)) {
			return statusPass, fmt.Sprintf("country of origin %q found", want)
		}
		return statusFail, fmt.Sprintf("country of origin %q not found (imported)", want)
	}
	return statusNeedsCorrection, "imported product; country of origin not asserted in application"
}

func checkWarningPresent(ctx *verifyContext) (status, string) {
	if strings.Contains(strings.ToLower(ctx.ocr), "government warning") {
		return statusPass, "government warning present"
	}
	return statusFail, "government warning missing"
}

func checkWarningWording(ctx *verifyContext) (status, string) {
	hay := normalizeWording(ctx.ocr)
	needle := normalizeWording(regs.GovernmentWarning)
	if strings.Contains(hay, needle) {
		return statusPass, "government warning wording matches 27 CFR 16.21"
	}
	if containsNear(hay, needle, 3) {
		return statusNeedsCorrection, "government warning wording near-matches (<=3 edits) but is not verbatim; human review required"
	}
	return statusFail, "government warning wording deviates from 27 CFR 16.21"
}

func checkWarningCaps(ctx *verifyContext) (status, string) {
	if strings.Contains(ctx.ocr, "GOVERNMENT WARNING") {
		return statusPass, "GOVERNMENT WARNING prefix is uppercase"
	}
	return statusFail, "GOVERNMENT WARNING prefix not uppercase (27 CFR 16.22(a)(2))"
}

// checkWarningFormat surfaces the 27 CFR 16.22(a)-(b) format requirements that
// are not machine-verifiable from OCR text — bold on the first two words
// (remainder not bold), minimum type size by container volume, maximum
// characters per inch, legibility, and contrasting background. It returns a
// non-blocking human_confirm flag so the verdict never silently claims the
// format was verified.
func checkWarningFormat(ctx *verifyContext) (status, string) {
	if !strings.Contains(strings.ToLower(ctx.ocr), "government warning") {
		return statusPass, "not applicable (no government warning present)"
	}
	return statusHumanConfirm, "warning format (bold/type size/contrast/legibility/characters-per-inch) not machine-verifiable from OCR; human confirm required (27 CFR 16.22(a)-(b))"
}

// reYellow5 matches a FD&C Yellow No. 5 disclosure, tolerant of "No. 5",
// "No 5", "#5", or a bare "5" after "yellow" (OCR mangles the ampersand).
var reYellow5 = regexp.MustCompile(`(?i)yellow\s*(no\.?\s*)?#?\s*5`)

// reContainVerb matches the "contains"/"contain" verb.
var reContainVerb = regexp.MustCompile(`(?i)\bcontains?\b`)

// reNegationWord matches a negation word inside a "contains <neg> X" clause.
var reNegationWord = regexp.MustCompile(`(?i)\b(?:no|without|free)\b`)

// reNegatedVerb matches a negation immediately before "contain(s)".
var reNegatedVerb = regexp.MustCompile(`(?i)(?:does\s+not|not)\s+$`)

// disclosureFound reports whether ocr affirmatively discloses the substance
// named by sub (a regex fragment, e.g. `sul(?:f|ph)it`): an affirmative
// "contains X" / "contain X" clause with no negation in the clause. Negated
// forms — "X free", "no X", "free of X", "without X", "contains no X", and
// "does not contain X" — do NOT count as a disclosure (27 CFR 5.63(c)).
func disclosureFound(ocr, sub string) bool {
	subRe := regexp.MustCompile(`(?i)` + sub)
	for _, v := range reContainVerb.FindAllStringIndex(ocr, -1) {
		if reNegatedVerb.MatchString(ocr[:v[0]]) {
			continue // "does not contain" / "not contain"
		}
		clause := ocr[v[1]:]
		if i := strings.IndexAny(clause, ".;\n"); i >= 0 {
			clause = clause[:i]
		}
		if m := subRe.FindStringIndex(clause); m != nil {
			if reNegationWord.MatchString(clause[:m[0]]) {
				continue // "contains no X" / "contains without X" / "contains free of X"
			}
			return true
		}
	}
	return false
}

func checkSulfiteDisclosure(ctx *verifyContext) (status, string) {
	disclosed := disclosureFound(ctx.ocr, `sul(?:f|ph)it`)
	if ctx.app.ContainsSulfites {
		if disclosed {
			return statusPass, "sulfite disclosure present"
		}
		return statusFail, `sulfites declared but "Contains sulfites" disclosure missing (27 CFR 5.63(c)(7))`
	}
	if disclosed {
		return statusNeedsCorrection, "sulfite disclosure present on label but not declared in application"
	}
	return statusPass, "not applicable (sulfites not declared)"
}

func checkAspartameDisclosure(ctx *verifyContext) (status, string) {
	present := strings.Contains(strings.ToUpper(ctx.ocr), regs.AspartameDisclosure)
	if ctx.app.ContainsAspartame {
		if strings.Contains(ctx.ocr, regs.AspartameDisclosure) {
			return statusPass, "aspartame disclosure present (all caps)"
		}
		if present {
			return statusFail, "aspartame disclosure present but not all caps (27 CFR 5.63(c)(8))"
		}
		return statusFail, "aspartame declared but disclosure missing (27 CFR 5.63(c)(8))"
	}
	if present {
		return statusNeedsCorrection, "aspartame disclosure present on label but not declared in application"
	}
	return statusPass, "not applicable (aspartame not declared)"
}

func checkFDACYellow5Disclosure(ctx *verifyContext) (status, string) {
	disclosed := reYellow5.MatchString(ctx.ocr)
	if ctx.app.ContainsFDACYellow5 {
		if disclosed {
			return statusPass, "FD&C Yellow No. 5 disclosure present"
		}
		return statusFail, "FD&C Yellow No. 5 declared but disclosure missing (27 CFR 5.63(c)(5))"
	}
	if disclosed {
		return statusNeedsCorrection, "FD&C Yellow No. 5 disclosure present on label but not declared in application"
	}
	return statusPass, "not applicable (FD&C Yellow No. 5 not declared)"
}

func checkCarmineDisclosure(ctx *verifyContext) (status, string) {
	disclosed := disclosureFound(ctx.ocr, `(?:carmine|cochineal)`)
	if ctx.app.ContainsCarmine {
		if disclosed {
			return statusPass, "cochineal/carmine disclosure present"
		}
		return statusFail, "cochineal/carmine declared but disclosure missing (27 CFR 5.63(c)(6))"
	}
	if disclosed {
		return statusNeedsCorrection, "cochineal/carmine disclosure present on label but not declared in application"
	}
	return statusPass, "not applicable (cochineal/carmine not declared)"
}

// reAgeClaim matches an age claim ("Aged 12 Years", "aged 10 years old") so
// the reverse direction can flag a label that states an age the application
// does not assert.
var reAgeClaim = regexp.MustCompile(`(?i)\baged\s+\d+`)

func checkAgeStatement(ctx *verifyContext) (status, string) {
	age := strings.TrimSpace(ctx.app.AgeStatement)
	if age == "" {
		if reAgeClaim.MatchString(ctx.ocr) {
			return statusNeedsCorrection, "age statement present on label but not declared in application"
		}
		return statusPass, "not applicable (no age statement asserted)"
	}
	if containsPhrase(normalizeFuzzy(ctx.ocr), normalizeFuzzy(age)) {
		return statusPass, fmt.Sprintf("age statement %q found", age)
	}
	return statusFail, fmt.Sprintf("age statement %q not found on label (27 CFR 5.63(c)(3))", age)
}

// reColoring matches a coloring/treatment disclosure ("colored with caramel",
// "treated with wood chips", "artificially colored", "caramel coloring").
// It deliberately excludes a bare "color"/"colour" noun ("amber color"), which
// describes appearance rather than disclosing a colorant (27 CFR 5.63(c)(2)).
var reColoring = regexp.MustCompile(`(?i)\b(colou?r(?:ed|ing)|treated)\b`)

func checkColoringDisclosure(ctx *verifyContext) (status, string) {
	disclosed := reColoring.MatchString(ctx.ocr)
	if ctx.app.Colored {
		if disclosed {
			return statusPass, "coloring/treatment disclosure present"
		}
		return statusFail, "coloring/treatment declared but disclosure missing (27 CFR 5.63(c)(2))"
	}
	if disclosed {
		return statusNeedsCorrection, "coloring/treatment disclosure present on label but not declared in application"
	}
	return statusPass, "not applicable (no coloring/treatment declared)"
}

func checkNeutralSpiritsDisclosure(ctx *verifyContext) (status, string) {
	disclosed := strings.Contains(strings.ToLower(ctx.ocr), "neutral spirit")
	if ctx.app.ContainsNeutralSpirits {
		if disclosed {
			return statusPass, "neutral spirits disclosure present"
		}
		return statusFail, "neutral spirits declared but %/commodity disclosure missing (27 CFR 5.63(c)(1))"
	}
	if disclosed {
		return statusNeedsCorrection, "neutral spirits disclosure present on label but not declared in application"
	}
	return statusPass, "not applicable (neutral spirits not declared)"
}

// reDistilledIn matches a state-of-distillation disclosure ("Distilled in
// Kentucky") for the reverse direction.
var reDistilledIn = regexp.MustCompile(`(?i)\bdistilled\s+in\b`)

func checkStateOfDistillation(ctx *verifyContext) (status, string) {
	state := strings.TrimSpace(ctx.app.StateOfDistillation)
	if state == "" {
		if reDistilledIn.MatchString(ctx.ocr) {
			return statusNeedsCorrection, "state of distillation present on label but not declared in application"
		}
		return statusPass, "not applicable (no state of distillation asserted)"
	}
	if containsPhrase(normalizeFuzzy(ctx.ocr), normalizeFuzzy(state)) {
		return statusPass, fmt.Sprintf("state of distillation %q found", state)
	}
	return statusFail, fmt.Sprintf("state of distillation %q not found on label (27 CFR 5.63(c)(4))", state)
}

func checkMaltAlcoholDesignation(ctx *verifyContext) (status, string) {
	if ctx.app.ProductType != "malt_beverages" {
		return statusPass, "not applicable (not a malt beverage)"
	}
	abv, found := findABV(ctx.ocr)
	if !found {
		// "N% ABV" is a non-legal form (checkAlcoholContent flags it), but its
		// numeric value still constrains the low/non-alcoholic thresholds.
		abv, found = findABVAcronym(ctx.ocr)
	}
	if !found {
		// A bare "N%" (no qualifier): on a low/non-alcoholic label the
		// percentage is the stated ABV, and the threshold must still be
		// checked rather than silently skipped.
		abv, found = findABVBarePct(ctx.ocr)
	}
	if reNonAlcoholic.MatchString(ctx.ocr) {
		if found && abv >= regs.MaltNonAlcoholicMaxABV {
			return statusFail, fmt.Sprintf("'non-alcoholic'/'alcohol free' requires <0.5%% ABV, found %.1f%% (27 CFR 7.65)", abv)
		}
		return statusPass, "non-alcoholic designation consistent"
	}
	if reLowAlcohol.MatchString(ctx.ocr) {
		if found && abv >= regs.MaltLowAlcoholMaxABV {
			return statusFail, fmt.Sprintf("'low alcohol' requires <2.5%% ABV, found %.1f%% (27 CFR 7.65)", abv)
		}
		return statusPass, "low alcohol designation consistent"
	}
	return statusPass, "not applicable (no low/non-alcoholic designation)"
}

// ---------------------------------------------------------------------------
// edit distance
// ---------------------------------------------------------------------------

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	curr := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		curr[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 0
			if ra[i-1] != rb[j-1] {
				cost = 1
			}
			curr[j] = min3(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(rb)]
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}

// containsNear reports whether needle appears in haystack within maxDist edits.
// Windows of len(needle)±maxDist are tried so insertions/deletions (which
// change length) still match; substitutions match at equal length.
func containsNear(haystack, needle string, maxDist int) bool {
	if needle == "" {
		return false
	}
	h, n := []rune(haystack), []rune(needle)
	for wl := len(n) - maxDist; wl <= len(n)+maxDist; wl++ {
		if wl < 1 || wl > len(h) {
			continue
		}
		for i := 0; i+wl <= len(h); i++ {
			if levenshtein(string(h[i:i+wl]), needle) <= maxDist {
				return true
			}
		}
	}
	return false
}
