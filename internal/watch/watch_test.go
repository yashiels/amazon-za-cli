package watch

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/yashiels/amazon-za-cli/internal/product"
)

func store(t *testing.T) Store {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return Store{Path: filepath.Join(dir, "config", "watch.json")}
}
func zar(amount float64) *product.Money { return &product.Money{Amount: amount, Currency: "ZAR"} }

func TestBaselineLowestAndTargets(t *testing.T) {
	s := store(t)
	now := time.Now().UTC()
	target := 180.0
	entry, err := s.Add("B0H471LF7H", zar(199), &target, now)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Baseline.Amount != 199 || entry.LowestSeen.Amount != 199 {
		t.Fatal("wrong baseline")
	}
	fetch := func(price *product.Money) func([]string) ([]product.Product, error) {
		return func(asins []string) ([]product.Product, error) {
			if len(asins) != 1 {
				t.Fatal(asins)
			}
			p := product.Bare(asins[0])
			p.Price = price
			return []product.Product{p}, nil
		}
	}
	results, err := s.Check(fetch(zar(175)), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	r := results[0]
	if r.Baseline.Amount != 199 || r.LowestSeen.Amount != 175 || *r.Drop != 24 || !r.HitTarget {
		t.Fatal("drop semantics incorrect")
	}
	results, err = s.Check(fetch(zar(190)), now.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if results[0].LowestSeen.Amount != 175 || results[0].Baseline.Amount != 199 || results[0].HitTarget {
		t.Fatal("baseline changed on check")
	}
	before := *results[0].LastChecked
	results, err = s.Check(fetch(nil), now.Add(3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Current != nil || !results[0].LastChecked.Equal(before) || results[0].LowestSeen.Amount != 175 {
		t.Fatal("unavailable price changed state")
	}
	if _, err := s.Add("B0H471LF7H", zar(180), nil, now.Add(4*time.Hour)); err != nil {
		t.Fatal(err)
	}
	entries, err := s.List()
	if err != nil || len(entries) != 1 || entries[0].Baseline.Amount != 180 || entries[0].Target != nil {
		t.Fatal("add did not reset baseline")
	}
	if err := s.Remove("B0H471LF7H"); err != nil {
		t.Fatal(err)
	}
	entries, err = s.List()
	if err != nil || len(entries) != 0 {
		t.Fatal("remove failed")
	}
}

func TestWatchLimitAndConcurrentUpdates(t *testing.T) {
	s := store(t)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.Add(fmt.Sprintf("B%09d", i), zar(100), nil, time.Now())
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	entries, err := s.List()
	if err != nil || len(entries) != 10 {
		t.Fatal("lost concurrent updates")
	}
	err = s.Update(func(entries []Entry) ([]Entry, error) {
		for i := 10; i < 100; i++ {
			entries = append(entries, Entry{ASIN: fmt.Sprintf("B%09d", i), Baseline: *zar(100), LowestSeen: *zar(100), AddedAt: time.Now()})
		}
		return entries, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add("B000000100", zar(100), nil, time.Now()); err == nil {
		t.Fatal("allowed watch 101")
	}
	if _, err := s.Add("B000000001", zar(90), nil, time.Now()); err != nil {
		t.Fatal("replacement refused at capacity")
	}
}
