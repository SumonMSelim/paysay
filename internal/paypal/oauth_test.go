package paypal

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchAccessTokenSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "test_client" || pass != "test_secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "client_credentials" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"access_token":"A21A-mock-token","token_type":"Bearer","expires_in":32400}`))
	}))
	defer ts.Close()

	tok, err := fetchTokenFromURL(context.Background(), ts.URL, "test_client", "test_secret", ts.Client())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok.AccessToken != "A21A-mock-token" {
		t.Errorf("got token %q, want A21A-mock-token", tok.AccessToken)
	}
	if tok.ExpiresIn != 32400 {
		t.Errorf("got expires_in %d, want 32400", tok.ExpiresIn)
	}
}

func TestFetchAccessTokenUnauthorized(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
	}))
	defer ts.Close()

	_, err := fetchTokenFromURL(context.Background(), ts.URL, "bad_client", "bad_secret", ts.Client())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
