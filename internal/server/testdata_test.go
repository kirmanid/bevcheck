package server

import (
	"testing"

	"Bevcheck/internal/fixtures"
	"Bevcheck/internal/labelgen"
)

// TestCasesMatchExpected re-derives each testdata fixture's verdict from the
// same source genfixtures uses, so the committed label.png + application.json
// pairs cannot drift from the verification logic. minConf=99 models a clean
// OCR pass (the fixtures exercise criteria, not OCR quality).
func TestCasesMatchExpected(t *testing.T) {
	for _, tc := range fixtures.Cases() {
		t.Run(tc.Name, func(t *testing.T) {
			app := tc.App
			ocr := labelgen.Text(tc.Fields, tc.Defects)
			overall, results := evaluate(&verifyContext{app: &app, ocr: ocr, minConf: 99})
			if overall != tc.Expected {
				t.Fatalf("overall = %q, want %q\nocr: %q\nresults: %+v", overall, tc.Expected, ocr, results)
			}
		})
	}
}
