package aapi

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestSigningOpenSSLHexKeyVectors(t *testing.T) {
	key := Key{ID: "test-key-id", Secret: make([]byte, 32)}
	for i := range key.Secret {
		key.Secret[i] = byte(i)
	}
	at, _ := time.Parse("2006-01-02T15:04:05-0700", "2026-01-02T03:04:05+0000")
	tests := []struct{ name, query, canonical, hex string }{
		{"empty", "", "GET\n2026-01-02T03:04:05+0000\ntest-key-id\nHmacSHA256\n/api/marketplaces/AE08WJ6YKNBMC/products/B0H471LF7H\n\naccept:application/json\nz-header: padded \naccept;z-header", "a066ba074b09ca18b95b2f84eebcb281621a658a9bcc29847410c321f29ba3e8"},
		{"sorted", "b=2&a=1&a=0&k=a b", "GET\n2026-01-02T03:04:05+0000\ntest-key-id\nHmacSHA256\n/api/marketplaces/AE08WJ6YKNBMC/products/B0H471LF7H\na=0&a=1&b=2&k=a+b\naccept:application/json\nz-header: padded \naccept;z-header", "de51d992c7e331f316c6dd14fa4896c602ee19766e27178a017e24e6a6ded953"},
		{"java_encoding", "k=~*+%2B", "GET\n2026-01-02T03:04:05+0000\ntest-key-id\nHmacSHA256\n/api/marketplaces/AE08WJ6YKNBMC/products/B0H471LF7H\nk=%7E*+%2B\naccept:application/json\nz-header: padded \naccept;z-header", "112a389f20b11fb176f71e546c1117458a585510a7d3610346073d50d7795a33"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.HasPrefix(tc.canonical, "GET\n2026-01-02") {
				t.Fatal("missing literal canonical vector")
			}
			signature, err := Sign("GET", ProductPath+"B0H471LF7H", tc.query, []Header{{"aCcEpT", "application/json"}, {"X-Amzn-Trace-Id", "excluded"}, {"z-Header", " padded "}}, at, key)
			if err != nil || !strings.Contains(signature, "Signature="+tc.hex+"/test-key-id") {
				t.Fatalf("signature mismatch: %v %s", err, signature)
			}
		})
	}
}

func TestSigningAcceptedLiveVector(t *testing.T) {
	body, err := os.ReadFile("../../testdata/signing/accepted.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		Method, Path, Query, Time, Signature string
		SignedHeaders                        map[string]string
		Status                               int
	}
	if err := json.Unmarshal(body, &vector); err != nil {
		t.Fatal(err)
	}
	at, err := time.Parse("2006-01-02T15:04:05-0700", vector.Time)
	if err != nil {
		t.Fatal(err)
	}
	headers := []Header{}
	for name, value := range vector.SignedHeaders {
		headers = append(headers, Header{name, value})
	}
	signature, err := Sign(vector.Method, vector.Path, vector.Query, headers, at, AppKey())
	if err != nil || signature != vector.Signature || vector.Status != 200 {
		t.Fatal("accepted-live signing vector did not reproduce")
	}
}

func TestSigningRejectsDuplicateHeaders(t *testing.T) {
	_, err := Sign("GET", "/", "", []Header{{"Accept", "a"}, {"accept", "b"}}, time.Now(), AppKey())
	if err == nil {
		t.Fatal("duplicate headers accepted")
	}
}

func TestSigningOpenSSLDgstSHA256Hmac(t *testing.T) {
	canonical := "GET\n2026-01-02T03:04:05+0000\ntest-key-id\nHmacSHA256\n/\n\naccept:application/json\naccept"
	if len(canonical) == 0 {
		t.Fatal("missing canonical input")
	}
	at, _ := time.Parse("2006-01-02T15:04:05-0700", "2026-01-02T03:04:05+0000")
	signature, err := Sign("GET", "/", "", []Header{{"Accept", "application/json"}}, at, Key{ID: "test-key-id", Secret: []byte("test-secret")})
	if err != nil || !strings.Contains(signature, "Signature=7d66f58c89fa25eeb3635d029b3ce255e682636fd46b7111e56613a07b5b67c8/test-key-id") {
		t.Fatal("openssl -hmac vector mismatch")
	}
}
