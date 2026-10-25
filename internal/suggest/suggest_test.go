package suggest

import (
	"os"
	"testing"
)

func TestSuggestionsFixture(t *testing.T) {
	body, err := os.ReadFile("../../testdata/suggest.json")
	if err != nil {
		t.Fatal(err)
	}
	values, err := Parse(body)
	if err != nil || len(values) != 10 || values[0] != "ssd enclosure" {
		t.Fatal("wrong completions")
	}
	if _, err := Parse([]byte(`{}`)); err == nil {
		t.Fatal("invalid response accepted")
	}
}
