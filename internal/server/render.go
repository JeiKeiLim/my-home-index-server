package server

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"

	"github.com/JeiKeiLim/my-home-index-server/web"
)

// templates is the parsed template registry, populated at package init
// time from the embedded web/templates/ tree. Pre-parsing means a
// malformed template surfaces as a panic at startup (Iron Law #11
// — fail fast on misconfiguration) rather than as a 500 mid-flight.
//
// Only the dashboard and login pages live here today; the /ports
// fragment template ships with job-8 and is added then.
var templates *template.Template

func init() {
	t, err := parseTemplates(web.Templates)
	if err != nil {
		panic(fmt.Sprintf("server: parse templates: %v", err))
	}
	templates = t
}

func parseTemplates(efs fs.FS) (*template.Template, error) {
	root := template.New("")
	entries, err := fs.Glob(efs, "templates/*.html")
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("no templates found under templates/*.html")
	}
	for _, name := range entries {
		raw, err := fs.ReadFile(efs, name)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		// Use the basename so callers reference templates by their
		// filename (e.g. "login.html") rather than the full path.
		base := name[len("templates/"):]
		if _, err := root.New(base).Parse(string(raw)); err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
	}
	return root, nil
}

// renderTemplate executes the named template against data and returns
// the rendered bytes. Returning a buffer rather than writing directly
// to the ResponseWriter means a partial write cannot leak a half-page
// to the client when template execution errors mid-stream.
func renderTemplate(name string, data any) ([]byte, error) {
	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, name, data); err != nil {
		return nil, fmt.Errorf("server: render %s: %w", name, err)
	}
	return buf.Bytes(), nil
}
