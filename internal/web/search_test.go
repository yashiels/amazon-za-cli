package web

import (
	"os"
	"strings"
	"testing"
)

func TestSearchFixtureRankDedupeAndLimit(t *testing.T) {
	b, err := os.ReadFile("../../testdata/search_ssd_enclosure.html")
	if err != nil {
		t.Fatal(err)
	}
	asins, err := ParseSearch(b, 5)
	if err != nil || len(asins) != 5 {
		t.Fatalf("search: %v %v", asins, err)
	}
	seen := map[string]bool{}
	for _, asin := range asins {
		if seen[asin] {
			t.Fatal("duplicate result")
		}
		seen[asin] = true
	}
	body := []byte(`<div data-component-type="s-search-result" data-asin="B0H471LF7H"></div><div data-asin="B0H471LF7H" data-component-type="s-search-result"></div><div data-component-type="s-search-result" data-asin="INVALID"></div><div data-component-type="s-search-result" data-asin="B0H4727J8L"></div>`)
	result, err := ParseSearch(body, 2)
	if err != nil || strings.Join(result, ",") != "B0H471LF7H,B0H4727J8L" {
		t.Fatal("rank/limit after dedupe failed")
	}
}

func TestSearchWAFAndLimits(t *testing.T) {
	body, err := os.ReadFile("../../testdata/waf_challenge.html")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseSearch(body, 10); err == nil {
		t.Fatal("WAF accepted")
	}
	for _, limit := range []int{0, 49} {
		if _, err := ParseSearch(nil, limit); err == nil {
			t.Fatal("invalid limit accepted")
		}
	}
}
