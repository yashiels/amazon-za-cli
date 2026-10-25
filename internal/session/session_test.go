package session

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/yashiels/amazon-za-cli/internal/aapi"
	"github.com/yashiels/amazon-za-cli/internal/fault"
	"github.com/yashiels/amazon-za-cli/internal/persist"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestBootstrapCookiesAndCacheExpiry(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/" || r.Header.Get("User-Agent") != aapi.UserAgent || r.Header.Get("Cookie") != "" {
			t.Error("wrong bootstrap request")
		}
		http.SetCookie(w, &http.Cookie{Name: "session-id", Value: "synthetic-session", Expires: now.Add(48 * time.Hour)})
		http.SetCookie(w, &http.Cookie{Name: "ubid-acbza", Value: "synthetic-ubid", Expires: now.Add(24 * time.Hour)})
		w.WriteHeader(200)
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	tr := aapi.NewTransportWithRoundTripper(3, roundTrip(func(r *http.Request) (*http.Response, error) {
		copy := r.Clone(r.Context())
		copy.URL.Host = base.Host
		return server.Client().Transport.RoundTrip(copy)
	}))
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "cache", "session.json")
	s, err := Bootstrap(context.Background(), tr, path, now)
	if err != nil || calls != 1 || s.ID != "synthetic-session" || s.UBID != "synthetic-ubid" || !s.ExpiresAt.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("bootstrap result: %v", err)
	}
	if _, valid, err := Cached(path, now.Add(time.Hour)); err != nil || !valid {
		t.Fatal("valid cache missed")
	}
	if _, valid, err := Cached(path, now.Add(25*time.Hour)); err != nil || valid {
		t.Fatal("expired cache used")
	}
	s.ExpiresAt = now.Add(30 * 24 * time.Hour)
	if err := persist.Write(path, s); err != nil {
		t.Fatal(err)
	}
	if _, valid, err := Cached(path, now.Add(8*24*time.Hour)); err != nil || valid {
		t.Fatal("seven day cap missed")
	}
}

func TestBootstrapNoFallbackAndWAF(t *testing.T) {
	for _, status := range []int{200, 202, 302, 403} {
		calls := 0
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls++
			w.Header().Set("Location", "https://www.amazon.co.za/")
			w.WriteHeader(status)
		}))
		base, _ := url.Parse(server.URL)
		tr := aapi.NewTransportWithRoundTripper(3, roundTrip(func(r *http.Request) (*http.Response, error) {
			copy := r.Clone(r.Context())
			copy.URL.Host = base.Host
			return server.Client().Transport.RoundTrip(copy)
		}))
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		_, err = Bootstrap(context.Background(), tr, filepath.Join(dir, "session.json"), time.Now())
		var e *fault.Error
		if !errors.As(err, &e) || calls != 1 {
			t.Fatal("bootstrap did not stop")
		}
		if status == 202 && e.Code != "blocked_by_waf" {
			t.Fatal("WAF lost")
		}
		if status != 202 && e.Code != "session_bootstrap_failed" {
			t.Fatal("unexpected bootstrap code")
		}
		server.Close()
	}
}
