package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerSetsETagAndCacheControl(t *testing.T) {
	h := Handler()

	// "/" resolves to index.html; styles.css is a plain non-vendor asset. Both
	// must carry an ETag and no-cache.
	for _, path := range []string{"/", "/styles.css"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", path, rr.Code)
		}
		etag := rr.Header().Get("ETag")
		if etag == "" || !strings.HasPrefix(etag, `"`) {
			t.Fatalf("%s missing/invalid ETag: %q", path, etag)
		}
		if cc := rr.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("%s non-vendor Cache-Control = %q, want no-cache", path, cc)
		}

		// If-None-Match with the same ETag yields 304.
		rr2 := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("If-None-Match", etag)
		h.ServeHTTP(rr2, req)
		if rr2.Code != http.StatusNotModified {
			t.Errorf("%s If-None-Match status = %d, want 304", path, rr2.Code)
		}
	}
}

func TestHandlerVendorCacheControl(t *testing.T) {
	h := Handler()
	// The Bryntum vendor directory is copied into the embedded FS at build time.
	// If it is absent (unit test without the Docker build), skip rather than
	// fail, but still assert the rule holds when a vendor file is present.
	etags := computeETags(subFS())
	var vendorName string
	for name := range etags {
		if strings.HasPrefix(name, "vendor/") {
			vendorName = name
			break
		}
	}
	if vendorName == "" {
		t.Skip("no vendor assets embedded in this build")
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/"+vendorName, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for %s", rr.Code, vendorName)
	}
	if cc := rr.Header().Get("Cache-Control"); cc != "public, max-age=604800" {
		t.Errorf("vendor Cache-Control = %q, want public, max-age=604800", cc)
	}
	if rr.Header().Get("ETag") == "" {
		t.Error("vendor file missing ETag")
	}
}

func TestMatchETag(t *testing.T) {
	cases := []struct {
		header, etag string
		want         bool
	}{
		{``, `"abc"`, false},
		{`"abc"`, `"abc"`, true},
		{`"x", "abc"`, `"abc"`, true},
		{`W/"abc"`, `"abc"`, true},
		{`*`, `"abc"`, true},
		{`"nope"`, `"abc"`, false},
	}
	for _, c := range cases {
		if got := matchETag(c.header, c.etag); got != c.want {
			t.Errorf("matchETag(%q, %q) = %v, want %v", c.header, c.etag, got, c.want)
		}
	}
}
