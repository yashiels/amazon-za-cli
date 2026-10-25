package product

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFullProductFixture(t *testing.T) {
	products, err := Parse(fixture(t, "product_full.json"), []string{"B0H471LF7H"})
	if err != nil {
		t.Fatal(err)
	}
	p := products[0]
	if p.Title == nil || !strings.Contains(*p.Title, "SSD Enclosure") || p.Brand == nil || *p.Brand != "Global Cellular" {
		t.Fatal("title/brand missing")
	}
	if p.Price == nil || p.Price.Amount != 199 || p.Price.Currency != "ZAR" || p.ListPrice != nil {
		t.Fatal("wrong price")
	}
	if p.Availability == nil || p.Availability.Type != "IN_STOCK" || p.Availability.Message != "In stock" {
		t.Fatal("wrong availability")
	}
	if p.Delivery == nil || *p.Delivery.Fastest != "2026-10-25T18:00:00.000Z" || *p.Delivery.Standard != "2026-10-26T18:00:00.000Z" {
		t.Fatal("wrong delivery promises")
	}
	if p.Rating == nil || p.Rating.Value != 4 || p.Rating.Count != 7 || p.Rating.Histogram["5"] != 53 {
		t.Fatal("wrong review summary")
	}
	if p.Category == nil || *p.Category != "HARD_DRIVE_ENCLOSURE" || len(p.Specs) < 8 {
		t.Fatal("category/specs missing")
	}
	if len(p.Images) != 9 || p.Images[0] != "https://m.media-amazon.com/images/I/41NI7X+0o8L.jpg" || !strings.Contains(p.Images[2], "61Xjpiu42HL") {
		t.Fatal("wrong images")
	}
	if !p.Hydrated || len(p.Warnings) != 0 || p.URL != "https://www.amazon.co.za/dp/B0H471LF7H" {
		t.Fatal("wrong envelope")
	}
	found := false
	for _, s := range p.Specs {
		if s.Label == "Hardware interface" && s.Value == "USB Type C" {
			found = true
		}
	}
	if !found {
		t.Fatal("spec fragments not decoded")
	}
}

func TestMixedDeniedFixture(t *testing.T) {
	products, err := Parse(fixture(t, "product_mixed_403.json"), []string{"B0H471LF7H"})
	if err != nil {
		t.Fatal(err)
	}
	p := products[0]
	if p.Price == nil || p.Title == nil || len(p.Warnings) != 1 {
		t.Fatal("allowed inclusions lost")
	}
	w := p.Warnings[0]
	if w.ASIN != p.ASIN || w.Field != "buyingOptions[0].dealDetails" || w.Status != 403 || w.Code != "403.1" {
		t.Fatalf("wrong warning: %+v", w)
	}
}

func TestBatchCorrelationMissingAndItemError(t *testing.T) {
	products, err := Parse(fixture(t, "batch_4_with_bogus.json"), []string{"B06XWRRMYX", "B0H471LF7H", "B06XWRRMYX", "B000000000", "B099999999"})
	if err != nil {
		t.Fatal(err)
	}
	if len(products) != 4 || products[0].ASIN != "B06XWRRMYX" || products[0].Price.Amount != 190 || products[0].ListPrice.Amount != 269 || products[1].Price.Amount != 199 {
		t.Fatal("batch correlation incorrect")
	}
	if products[2].Error == nil || products[2].Error.Status != 404 || products[2].Error.Code != "404.2" || products[3].Error.Code != "not_found" {
		t.Fatal("item error/missing entity not represented")
	}
}

func TestNestedErrorsNullFields(t *testing.T) {
	var root map[string]any
	if err := json.Unmarshal(fixture(t, "product_full.json"), &root); err != nil {
		t.Fatal(err)
	}
	entity := root["entity"].(map[string]any)
	denied := func() any {
		return map[string]any{"type": "error/v1", "entity": map[string]any{"status": 403, "code": "403.1"}}
	}
	entity["title"] = denied()
	option := entity["buyingOptions"].([]any)[0].(map[string]any)
	option["availability"] = denied()
	option["price"] = denied()
	body, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	items, err := Parse(body, []string{"B0H471LF7H"})
	if err != nil {
		t.Fatal(err)
	}
	p := items[0]
	if p.Title != nil || p.Availability != nil || p.Price != nil || len(p.Warnings) != 3 || p.Rating == nil {
		t.Fatal("denied fields not independently nulled")
	}
	fields := map[string]bool{}
	for _, w := range p.Warnings {
		fields[w.Field] = true
	}
	if !fields["title"] || !fields["buyingOptions[0].availability"] || !fields["buyingOptions[0].price"] {
		t.Fatal(fields)
	}
}

func TestASINInputs(t *testing.T) {
	valid := []string{"B0H471LF7H", "https://www.amazon.co.za/dp/B0H471LF7H?ref=x", "https://amazon.co.za/gp/product/B0H471LF7H", "https://www.amazon.co.za/gp/aw/d/B0H471LF7H", "https://www.amazon.co.za/enclosure/dp/B0H471LF7H"}
	for _, s := range valid {
		asin, err := ASIN(s)
		if err != nil || asin != "B0H471LF7H" {
			t.Errorf("rejected %s", s)
		}
	}
	invalid := []string{"https://amzn.to/abc", "https://evil.test/dp/B0H471LF7H", "https://www.amazon.co.za:443/dp/B0H471LF7H", "https://user@www.amazon.co.za/dp/B0H471LF7H", "https://www.amazon.co.za/dp/B0H471LF7H%2f", "https://www.amazon.co.za/../dp/B0H471LF7H", "https://www.amazon.co.za/cart", "b0h471lf7h", "https://www.amazon.co.za/dp/B0H471LF7H/extra"}
	for _, s := range invalid {
		if _, err := ASIN(s); err == nil {
			t.Errorf("accepted %s", s)
		}
	}
}
