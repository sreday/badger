// Package ui holds the header partials every Badger tool hosted by Snout
// shares (brand.html): the badger head with the tool's name, and the
// buttons to the other tools. Snout, Sniff and Snoop add it to each of
// their page templates with Parse and give their templates Tool as "tool".
package ui

import (
	"embed"
	"html/template"
)

//go:embed brand.html
var files embed.FS

// Parse adds the "brand", "brandcss", "apps" and "appscss" templates to t.
func Parse(t *template.Template) (*template.Template, error) {
	return t.ParseFS(files, "brand.html")
}

// Tool is the template func "tool": the header brand's name, subtitle and
// optional version, as in {{template "brand" (tool "snout" "by badger")}}.
func Tool(name, sub string, ver ...string) map[string]string {
	v := ""
	if len(ver) > 0 {
		v = ver[0]
	}
	return map[string]string{"Name": name, "Sub": sub, "Ver": v}
}
