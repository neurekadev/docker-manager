// Package web embeds the SvelteKit PWA into the manager binary.
//
// Layout (see docs/internal/development.md):
//
//	web/build/fallback/  committed placeholder page (always present)
//	web/build/app/       `npm run build` output (git-ignored)
//
// SvelteKit (adapter-static) writes only to build/app and never deletes the
// committed build/fallback, so `//go:embed all:build` always compiles: a
// clean checkout builds and tests without Node, and a binary built after
// `npm run build` (as the manager image does) serves the real UI.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:build
var build embed.FS

// Assets returns the UI to serve and whether it is the real SvelteKit build
// (false means the placeholder page).
func Assets() (fs.FS, bool) {
	if app, err := fs.Sub(build, "build/app"); err == nil {
		if _, err := fs.Stat(app, "index.html"); err == nil {
			return app, true
		}
	}
	fallback, err := fs.Sub(build, "build/fallback")
	if err != nil {
		panic(err) // unreachable: the directory is embedded at compile time
	}
	return fallback, false
}
