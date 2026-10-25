package watch

import (
	"errors"
	"math"
	"os"
	"time"

	"github.com/yashiels/amazon-za-cli/internal/aapi"
	"github.com/yashiels/amazon-za-cli/internal/fault"
	"github.com/yashiels/amazon-za-cli/internal/persist"
	"github.com/yashiels/amazon-za-cli/internal/product"
)

type Entry struct {
	ASIN        string        `json:"asin"`
	Baseline    product.Money `json:"baseline"`
	LowestSeen  product.Money `json:"lowestSeen"`
	Target      *float64      `json:"target,omitempty"`
	AddedAt     time.Time     `json:"addedAt"`
	LastChecked *time.Time    `json:"lastChecked"`
}

type Result struct {
	Entry
	Current   *product.Money     `json:"current"`
	Drop      *float64           `json:"drop"`
	HitTarget bool               `json:"hitTarget"`
	Error     *product.ItemError `json:"error,omitempty"`
}

type Store struct{ Path string }

func (s Store) List() ([]Entry, error) {
	entries := []Entry{}
	err := persist.Read(s.Path, &entries)
	if errors.Is(err, os.ErrNotExist) {
		return entries, nil
	}
	if err != nil {
		return nil, err
	}
	if len(entries) > 100 {
		return nil, fault.New("storage_error", "invalid watchlist")
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if !aapi.ValidASIN(e.ASIN) || seen[e.ASIN] || !validMoney(e.Baseline) || !validMoney(e.LowestSeen) || e.Target != nil && (!finite(*e.Target) || *e.Target < 0) {
			return nil, fault.New("storage_error", "invalid watchlist")
		}
		seen[e.ASIN] = true
	}
	return entries, nil
}
func finite(n float64) bool { return !math.IsNaN(n) && !math.IsInf(n, 0) }
func validMoney(m product.Money) bool {
	return m.Currency == "ZAR" && finite(m.Amount) && m.Amount >= 0
}

func (s Store) Update(fn func([]Entry) ([]Entry, error)) error {
	unlock, err := persist.Lock(s.Path+".lock", 5*time.Second)
	if err != nil {
		return err
	}
	defer unlock()
	entries, err := s.List()
	if err != nil {
		return err
	}
	entries, err = fn(entries)
	if err != nil {
		return err
	}
	return persist.Write(s.Path, entries)
}

func (s Store) Add(asin string, price *product.Money, target *float64, now time.Time) (Entry, error) {
	if !aapi.ValidASIN(asin) || target != nil && (!finite(*target) || *target < 0) {
		return Entry{}, fault.Usage()
	}
	if price == nil || !validMoney(*price) {
		return Entry{}, fault.New("price_unavailable", "price unavailable")
	}
	entry := Entry{ASIN: asin, Baseline: *price, LowestSeen: *price, Target: target, AddedAt: now}
	err := s.Update(func(entries []Entry) ([]Entry, error) {
		for i, e := range entries {
			if e.ASIN == asin {
				entries[i] = entry
				return entries, nil
			}
		}
		if len(entries) >= 100 {
			return nil, fault.New("watch_full", "watchlist full")
		}
		return append(entries, entry), nil
	})
	return entry, err
}

func (s Store) Remove(asin string) error {
	return s.Update(func(entries []Entry) ([]Entry, error) {
		filtered := make([]Entry, 0, len(entries))
		for _, e := range entries {
			if e.ASIN != asin {
				filtered = append(filtered, e)
			}
		}
		return filtered, nil
	})
}

func Evaluate(e Entry, p product.Product, now time.Time) Result {
	result := Result{Entry: e, Error: p.Error}
	if p.Price == nil || !validMoney(*p.Price) {
		if result.Error == nil {
			result.Error = &product.ItemError{Code: "price_unavailable"}
		}
		return result
	}
	result.Current = p.Price
	drop := math.Round((e.Baseline.Amount-p.Price.Amount)*100) / 100
	result.Drop = &drop
	result.HitTarget = e.Target != nil && p.Price.Amount <= *e.Target
	if p.Price.Amount < result.LowestSeen.Amount {
		result.LowestSeen = *p.Price
	}
	result.LastChecked = &now
	return result
}

func (s Store) Check(fetch func([]string) ([]product.Product, error), now time.Time) ([]Result, error) {
	results := []Result{}
	err := s.Update(func(entries []Entry) ([]Entry, error) {
		asins := make([]string, 0, len(entries))
		for _, e := range entries {
			asins = append(asins, e.ASIN)
		}
		products, err := fetch(asins)
		if err != nil {
			return nil, err
		}
		indexed := map[string]product.Product{}
		for _, p := range products {
			indexed[p.ASIN] = p
		}
		for i, e := range entries {
			p, ok := indexed[e.ASIN]
			if !ok {
				p = product.Bare(e.ASIN)
				p.Error = &product.ItemError{Code: "not_found"}
			}
			result := Evaluate(e, p, now)
			entries[i] = result.Entry
			results = append(results, result)
		}
		return entries, nil
	})
	return results, err
}
