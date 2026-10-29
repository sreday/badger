package pkg

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fogleman/gg"
)

// layoutCases are the names, titles and companies badges really get.
var layoutCases = []BadgeData{
	{Name: "Li Na", Title: "SRE", Company: "Grab"},
	{Name: "Mark Pawlikowski", Title: "Organizer", Company: "SREday"},
	{Name: "Ana", Title: "", Company: ""},
	{Name: "Christopher Montgomery", Title: "Staff Software Engineer", Company: "Datadog"},
	{Name: "María José Fernández de Córdoba", Title: "Head of Platform Engineering", Company: "Banco Santander"},
	{Name: "Maximiliana Konstantinopoulou-Abernathy", Title: "Principal Site Reliability Engineer, Observability Platform", Company: "International Business Machines Corporation"},
	{Name: "Paweł Łukasz Brzęczyszczykiewicz", Title: "CTO", Company: "Żabka Polska"},
	{Name: "Venkata Subrahmanya Lakshminarayana Chakravarthy", Title: "Senior Director of Infrastructure", Company: "Tata Consultancy Services"},
	{Name: "Jo", Title: "VP Engineering, Developer Experience & Internal Platforms", Company: "Mercedes-Benz Research & Development North America"},
	{Name: "Sophie Müller-Lüdenscheidt", Title: "Freelance", Company: "Self-employed"},
	{Name: "Alexander Hamilton", Title: "", Company: "Rijksdienst voor Ondernemend Nederland"},
	{Name: "Oluwaseun Adebayo-Ogunleye", Title: "Cloud Architect", Company: "Microsoft"},
}

func TestLayoutNeverCutsOrOverflows(t *testing.T) {
	if err := loadV2Fonts(); err != nil {
		t.Fatal(err)
	}
	dc := gg.NewContext(badgeWidth, badgeHeight)
	box := textBox{wide: 888, narrow: 504, wideH: 140, maxH: 520}
	for _, d := range layoutCases {
		var fields []badgeField
		for _, f := range []badgeField{{d.Name, roleName, true, 1}, {d.Title, roleTitle, false, .85}, {d.Company, roleCompany, true, .92}} {
			if f.text != "" {
				fields = append(fields, f)
			}
		}
		lines, tops, h := layoutText(dc, fields, box)
		var got []string
		for i, l := range lines {
			dc.SetFontFace(v2Face(l.bold, l.size))
			// Lines in the band above the QR may be wide, the rest narrow.
			width := box.narrow
			if tops[i]+float64(l.size)*1.1 <= box.wideH {
				width = box.wide
			}
			if w, _ := dc.MeasureString(l.text); w > width {
				t.Errorf("%s: %q is %.0f wide (max %.0f)", d.Name, l.text, w, width)
			}
			if strings.Contains(l.text, "…") || strings.Contains(l.text, "...") {
				t.Errorf("%s: cut short: %q", d.Name, l.text)
			}
			got = append(got, l.text)
		}
		// Every word printed, in order.
		want := strings.Join(strings.Fields(d.Name+" "+d.Title+" "+d.Company), " ")
		if all := strings.ReplaceAll(strings.Join(got, " "), "- ", "-"); all != want {
			t.Errorf("%s: printed %q, want %q", d.Name, all, want)
		}
		if h > box.maxH {
			t.Errorf("%s: %.0f tall (max %.0f)", d.Name, h, box.maxH)
		}
	}
}

func TestZZLayoutSheet(t *testing.T) {
	dir := os.Getenv("BADGE_PREVIEW_DIR")
	if dir == "" {
		t.Skip()
	}
	loadTestTemplate(t)
	for i, d := range layoutCases {
		d.LinkedIn = "https://www.linkedin.com/in/example-" + fmt.Sprint(i)
		img, err := GenerateBadgeV2(d)
		if err != nil {
			t.Fatal(err)
		}
		png, _ := EncodeBadgePNG(img)
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("layout-%02d.png", i)), png, 0644)
	}
}
