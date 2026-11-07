// Package web embeds the static assets for the PaySay demo UI so the binary
// is self-contained and judges can run a single container with no separate
// front-end build step.
package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed assets/*
var assets embed.FS

// FS returns an http.FileSystem rooted at the embedded assets directory.
func FS() http.FileSystem {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		// The embed directive guarantees assets/ exists at build time, so this
		// cannot happen in a correctly built binary.
		panic(err)
	}
	return http.FS(sub)
}
