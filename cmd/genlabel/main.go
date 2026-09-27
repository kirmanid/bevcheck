// Command genlabel renders a synthetic label image to a PNG file for testing.
// No AI, no external fonts — text is drawn with a built-in bitmap font.
package main

import (
	"flag"
	"fmt"
	"os"

	"Bevcheck/internal/labelgen"
)

func main() {
	out := flag.String("o", "label.png", "output PNG path")
	brand := flag.String("brand", "OLD TOM DISTILLERY", "brand name")
	class := flag.String("class", "Kentucky Straight Bourbon Whiskey", "class/type")
	abv := flag.String("abv", "45% Alc./Vol. (90 Proof)", "alcohol content")
	net := flag.String("net", "750 mL", "net contents")
	name := flag.String("name", "Bottled by Old Tom Distillery Co., Bardstown, KY", "name & address statement")
	origin := flag.String("origin", "", "country of origin (imports)")
	titleCase := flag.Bool("title-case-warning", false, "render warning prefix in title case (defect)")
	omitWarning := flag.Bool("omit-warning", false, "omit the warning (defect)")
	flag.Parse()

	b, err := labelgen.Render(labelgen.Fields{
		BrandName:       *brand,
		ClassType:       *class,
		AlcoholContent:  *abv,
		NetContents:     *net,
		NameAddress:     *name,
		CountryOfOrigin: *origin,
	}, labelgen.Defects{
		TitleCaseWarning: *titleCase,
		OmitWarning:      *omitWarning,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("wrote", *out)
}
