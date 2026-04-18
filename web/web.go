// Package web embeds the dashboard's HTML templates and static assets
// into the binary. Keeping the embed declarations in a leaf package
// preserves the on-disk layout (web/templates/*.html, web/static/*)
// that the design and spec call out, while letting internal/server
// import the assets as ordinary FS values.
package web

import "embed"

// Templates contains every *.html file under web/templates/. The
// server pre-parses this at startup; missing or malformed templates
// surface immediately.
//
//go:embed templates/*.html
var Templates embed.FS

// Static contains every file under web/static/ — htmx.min.js, the
// dashboard stylesheet, and any future assets. Served verbatim under
// /static/ by the server package.
//
//go:embed static/*
var Static embed.FS
