// Package labelgen renders synthetic alcohol-beverage labels as PNG images
// (pure Go, no AI, no external fonts). Used to build deterministic test
// fixtures for the OCR + verification pipeline.
package labelgen

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"

	"Bevcheck/internal/regs"
)

// Fields is the label content to render.
type Fields struct {
	BrandName       string
	ClassType       string
	AlcoholContent  string
	NetContents     string
	NameAddress     string
	CountryOfOrigin string
	Warning         string // defaults to regs.GovernmentWarning
	// Extra lines rendered after the standard fields (conditional disclosures,
	// designations, etc.). Order is preserved.
	Extra []string
}

// Defects injects deliberate violations for negative testing.
type Defects struct {
	OmitBrand        bool
	OmitClassType    bool
	OmitAlcohol      bool
	OmitNetContents  bool
	OmitNameAddress  bool
	OmitWarning      bool
	TitleCaseWarning bool // "Government Warning:" prefix instead of all-caps
}

// Lines returns the label's text lines in render order — the ground-truth text
// a clean OCR pass should recover.
func Lines(f Fields, d Defects) []string {
	warning := f.Warning
	if warning == "" {
		warning = regs.GovernmentWarning
	}
	if d.TitleCaseWarning {
		const prefix = "GOVERNMENT WARNING:"
		warning = "Government Warning:" + warning[len(prefix):]
	}

	var lines []string
	if !d.OmitBrand && f.BrandName != "" {
		lines = append(lines, f.BrandName)
	}
	if !d.OmitClassType && f.ClassType != "" {
		lines = append(lines, f.ClassType)
	}
	if !d.OmitAlcohol && f.AlcoholContent != "" {
		lines = append(lines, f.AlcoholContent)
	}
	if !d.OmitNetContents && f.NetContents != "" {
		lines = append(lines, f.NetContents)
	}
	if !d.OmitNameAddress && f.NameAddress != "" {
		lines = append(lines, f.NameAddress)
	}
	if f.CountryOfOrigin != "" {
		lines = append(lines, f.CountryOfOrigin)
	}
	lines = append(lines, f.Extra...)
	if !d.OmitWarning {
		lines = append(lines, wrap(warning, 60)...)
	}
	return lines
}

// Text returns the whitespace-collapsed full text (what clean OCR should yield).
func Text(f Fields, d Defects) string {
	return strings.TrimSpace(strings.Join(Lines(f, d), " "))
}

// Render draws the label to a PNG and returns the encoded bytes.
func Render(f Fields, d Defects) ([]byte, error) {
	img := render(Lines(f, d))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func render(lines []string) image.Image {
	const (
		pad   = 8
		lineH = 16
		adv   = 7 // basicfont.Face7x13.Advance
	)
	w := pad * 2
	for _, l := range lines {
		if wl := len(l)*adv + pad*2; wl > w {
			w = wl
		}
	}
	h := len(lines)*lineH + pad*2
	if h < 10 {
		h = 10
	}
	if w < 10 {
		w = 10
	}

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)

	d := &font.Drawer{
		Dst:  img,
		Src:  &image.Uniform{C: color.Black},
		Face: basicfont.Face7x13,
	}
	y := pad + basicfont.Face7x13.Ascent
	for _, l := range lines {
		d.Dot = fixed.P(pad, y)
		d.DrawString(l)
		y += lineH
	}
	return img
}

func wrap(s string, width int) []string {
	var out []string
	for len(s) > width {
		i := strings.LastIndex(s[:width], " ")
		if i < 0 {
			i = width
		}
		out = append(out, s[:i])
		s = strings.TrimSpace(s[i:])
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}
