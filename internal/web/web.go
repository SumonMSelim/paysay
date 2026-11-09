// Package web embeds the static assets for the PaySay demo UI so the binary
// is self-contained and judges can run a single container with no separate
// front-end build step.
package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed assets/*
var assets embed.FS

// subFS is the embedded asset tree rooted at assets/.
func subFS() fs.FS {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		// The embed directive guarantees assets/ exists at build time, so this
		// cannot happen in a correctly built binary.
		panic(err)
	}
	return sub
}

// FS returns an http.FileSystem rooted at the embedded assets directory.
func FS() http.FileSystem {
	return http.FS(subFS())
}

// Handler returns an http.Handler that serves the embedded assets with caching.
// It computes a content-hash ETag for every embedded file once at startup
// (including the Bryntum vendor bundle copied in at build time), honours
// If-None-Match with a 304, and sets Cache-Control so the large vendor bundle
// is not refetched on every visit:
//   - /vendor/* : public, max-age=604800 (7 days); these are content-addressed
//     by the ETag and change rarely.
//   - everything else : no-cache, so the browser always revalidates, which is
//     cheap because the ETag turns an unchanged file into a 304.
func Handler() http.Handler {
	root := subFS()
	etags := computeETags(root)
	fileServer := http.FileServer(http.FS(root))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := cleanName(r.URL.Path)
		if etag, ok := etags[name]; ok {
			w.Header().Set("ETag", etag)
			if strings.HasPrefix("/"+name, "/vendor/") {
				w.Header().Set("Cache-Control", "public, max-age=604800")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			if matchETag(r.Header.Get("If-None-Match"), etag) {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		fileServer.ServeHTTP(w, r)
	})
}

// cleanName maps a request path to the embedded file name used as the ETag key.
// A directory path ("/" or "/dir/") resolves to its index.html, mirroring what
// http.FileServer serves.
func cleanName(p string) string {
	p = strings.TrimPrefix(p, "/")
	if p == "" || strings.HasSuffix(p, "/") {
		p += "index.html"
	}
	return p
}

// computeETags walks the embedded tree and returns a map from file name to a
// strong content-hash ETag (a quoted hex SHA-256 prefix).
func computeETags(root fs.FS) map[string]string {
	etags := make(map[string]string)
	_ = fs.WalkDir(root, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		f, err := root.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return nil
		}
		etags[path] = `"` + hex.EncodeToString(h.Sum(nil))[:32] + `"`
		return nil
	})
	return etags
}

// matchETag reports whether the If-None-Match header matches the file's ETag.
// It handles a comma-separated list and the "*" wildcard, per RFC 7232.
func matchETag(ifNoneMatch, etag string) bool {
	if ifNoneMatch == "" {
		return false
	}
	for _, candidate := range strings.Split(ifNoneMatch, ",") {
		candidate = strings.TrimSpace(candidate)
		candidate = strings.TrimPrefix(candidate, "W/")
		if candidate == "*" || candidate == etag {
			return true
		}
	}
	return false
}
