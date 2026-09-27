// Command genfixtures generates the manual-testing fixtures under testdata/
// (one directory per case, each with label.png + application.json). Pure Go,
// deterministic; re-run to regenerate. The case list lives in
// internal/fixtures.Cases() so the server's testdata test re-derives each
// verdict from the same source.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"Bevcheck/internal/fixtures"
	"Bevcheck/internal/labelgen"
)

func main() {
	for _, tc := range fixtures.Cases() {
		dir := filepath.Join("testdata", tc.Name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			panic(err)
		}
		png, err := labelgen.Render(tc.Fields, tc.Defects)
		if err != nil {
			panic(fmt.Errorf("%s: render: %w", tc.Name, err))
		}
		if err := os.WriteFile(filepath.Join(dir, "label.png"), png, 0o644); err != nil {
			panic(err)
		}
		appJSON, err := json.MarshalIndent(tc.App, "", "  ")
		if err != nil {
			panic(err)
		}
		appJSON = append(appJSON, '\n')
		if err := os.WriteFile(filepath.Join(dir, "application.json"), appJSON, 0o644); err != nil {
			panic(err)
		}
		fmt.Printf("%-28s -> %-16s %s\n", tc.Name, tc.Expected, tc.Desc)
	}
}
