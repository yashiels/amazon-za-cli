package product

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/yashiels/amazon-za-cli/internal/aapi"
	"github.com/yashiels/amazon-za-cli/internal/fault"
)

type Money struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}
type Availability struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}
type Delivery struct {
	Fastest  *string `json:"fastest"`
	Standard *string `json:"standard"`
}
type Rating struct {
	Value     float64            `json:"value"`
	Count     int                `json:"count"`
	Histogram map[string]float64 `json:"histogram"`
}
type Spec struct {
	Label string `json:"label"`
	Value string `json:"value"`
}
type Warning struct {
	ASIN   string `json:"asin,omitempty"`
	Field  string `json:"field,omitempty"`
	Status int    `json:"status,omitempty"`
	Code   string `json:"code"`
}
type ItemError struct {
	Code   string `json:"code"`
	Status int    `json:"status,omitempty"`
}
type Product struct {
	ASIN         string        `json:"asin"`
	Title        *string       `json:"title"`
	Brand        *string       `json:"brand"`
	Price        *Money        `json:"price"`
	ListPrice    *Money        `json:"listPrice"`
	Availability *Availability `json:"availability"`
	Delivery     *Delivery     `json:"delivery"`
	Rating       *Rating       `json:"rating"`
	Category     *string       `json:"category"`
	Specs        []Spec        `json:"specs"`
	Images       []string      `json:"images"`
	URL          string        `json:"url"`
	Hydrated     bool          `json:"hydrated"`
	Warnings     []Warning     `json:"warnings"`
	Error        *ItemError    `json:"error,omitempty"`
}

type object = map[string]any

func obj(v any) object     { m, _ := v.(map[string]any); return m }
func array(v any) []any    { a, _ := v.([]any); return a }
func text(v any) string    { s, _ := v.(string); return s }
func number(v any) float64 { n, _ := v.(float64); return n }
func at(v any, keys ...string) any {
	for _, k := range keys {
		v = obj(v)[k]
	}
	return v
}
func pointer(v any) *string {
	s := text(v)
	if s == "" {
		return nil
	}
	return &s
}

func ASIN(input string) (string, error) {
	if aapi.ValidASIN(input) {
		return input, nil
	}
	u, err := url.Parse(input)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || (u.Host != "www.amazon.co.za" && u.Host != "amazon.co.za") {
		return "", fault.Usage()
	}
	parts := strings.Split(strings.TrimPrefix(strings.TrimSuffix(u.EscapedPath(), "/"), "/"), "/")
	asin := ""
	switch {
	case len(parts) == 2 && parts[0] == "dp":
		asin = parts[1]
	case len(parts) == 3 && parts[0] == "gp" && parts[1] == "product":
		asin = parts[2]
	case len(parts) == 4 && parts[0] == "gp" && parts[1] == "aw" && parts[2] == "d":
		asin = parts[3]
	case len(parts) == 3 && parts[0] != "" && parts[0] != "." && parts[0] != ".." && !strings.ContainsAny(parts[0], "%\\") && parts[1] == "dp":
		asin = parts[2]
	}
	if !aapi.ValidASIN(asin) {
		return "", fault.Usage()
	}
	return asin, nil
}

func Dedupe(asins []string) []string {
	result := make([]string, 0, len(asins))
	seen := map[string]bool{}
	for _, asin := range asins {
		if !seen[asin] {
			seen[asin] = true
			result = append(result, asin)
		}
	}
	return result
}

func Bare(asin string) Product {
	return Product{ASIN: asin, URL: "https://www.amazon.co.za/dp/" + asin, Warnings: []Warning{}}
}

func sanitize(v any, path, asin string, warnings *[]Warning) any {
	switch node := v.(type) {
	case map[string]any:
		if text(node["type"]) == "error/v1" {
			*warnings = append(*warnings, Warning{ASIN: asin, Field: path, Status: int(number(at(node, "entity", "status"))), Code: safeCode(at(node, "entity", "code"))})
			return nil
		}
		keys := make([]string, 0, len(node))
		for k := range node {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			next := k
			if k == "entity" {
				next = path
			} else if path != "" {
				next = path + "." + k
			}
			node[k] = sanitize(node[k], next, asin, warnings)
		}
	case []any:
		for i, child := range node {
			node[i] = sanitize(child, fmt.Sprintf("%s[%d]", path, i), asin, warnings)
		}
	}
	return v
}

func safeCode(v any) string {
	s := text(v)
	if len(s) == 0 || len(s) > 32 {
		return "inclusion_error"
	}
	for _, c := range s {
		if (c < '0' || c > '9') && c != '.' {
			return "inclusion_error"
		}
	}
	return s
}

func money(v any) *Money {
	m := obj(at(v, "moneyValueOrRange", "value"))
	if _, ok := m["amount"].(float64); !ok || text(m["currencyCode"]) == "" {
		return nil
	}
	return &Money{Amount: number(m["amount"]), Currency: text(m["currencyCode"])}
}

func fragments(v any) string {
	var b strings.Builder
	for _, f := range array(at(v, "displayContent", "fragments")) {
		b.WriteString(text(at(f, "text")))
	}
	return strings.TrimSpace(b.String())
}

func parseDelivery(v any) *Delivery {
	var earliest, standard string
	for _, instruction := range array(at(v, "messageInstructions")) {
		group := text(at(instruction, "group"))
		for _, data := range array(at(instruction, "messageData")) {
			dates := at(data, "promise", "deliveryDateValueOrRange")
			date := text(at(dates, "deliveryDate", "dateTime"))
			if date == "" {
				date = text(at(dates, "deliveryDateRange", "max", "dateTime"))
			}
			if _, err := time.Parse(time.RFC3339, date); err != nil {
				continue
			}
			if earliest == "" || date < earliest {
				earliest = date
			}
			if group == "primaryDeliveryMessage" {
				standard = date
			}
		}
	}
	if earliest == "" && standard == "" {
		return nil
	}
	return &Delivery{Fastest: pointer(earliest), Standard: pointer(standard)}
}

func parse(node object, asin string) Product {
	p := Bare(asin)
	if text(node["type"]) == "error/v1" {
		p.Error = &ItemError{Code: safeCode(at(node, "entity", "code")), Status: int(number(at(node, "entity", "status")))}
		return p
	}
	e := obj(node["entity"])
	if e == nil {
		p.Error = &ItemError{Code: "not_found"}
		return p
	}
	sanitize(e, "", asin, &p.Warnings)
	p.Hydrated = true
	p.Title = pointer(at(e, "title", "entity", "displayString"))
	for _, c := range array(at(e, "byLine", "entity", "contributors")) {
		for _, r := range array(at(c, "roles")) {
			if text(at(r, "type")) == "BRAND" {
				p.Brand = pointer(at(c, "name"))
				break
			}
		}
		if p.Brand != nil {
			break
		}
	}
	options := array(e["buyingOptions"])
	if len(options) > 0 {
		option := options[0]
		price := at(option, "price", "entity")
		p.Price = money(at(price, "priceToPay"))
		p.ListPrice = money(at(price, "basisPrice"))
		availability := at(option, "availability", "entity")
		if obj(availability) != nil {
			p.Availability = &Availability{Type: text(at(availability, "type")), Message: text(at(availability, "primaryMessage"))}
		}
		p.Delivery = parseDelivery(at(option, "delivery", "entity"))
	}
	reviews := at(e, "customerReviewsSummary", "entity")
	if obj(at(reviews, "rating")) != nil {
		p.Rating = &Rating{Value: number(at(reviews, "rating", "value")), Count: int(number(at(reviews, "count", "value"))), Histogram: map[string]float64{}}
		for i, name := range []string{"oneStar", "twoStar", "threeStar", "fourStar", "fiveStar"} {
			if value, ok := at(reviews, "histogram", name, "percentage").(float64); ok {
				p.Rating.Histogram[fmt.Sprint(i+1)] = value
			}
		}
	}
	p.Category = pointer(at(e, "productCategory", "entity", "productType"))
	overview := at(e, "productOverview", "entity")
	if overview != nil {
		p.Specs = []Spec{}
		seen := map[string]bool{}
		for _, group := range array(at(overview, "sectionGroups")) {
			for _, section := range array(at(group, "sections")) {
				for _, attribute := range array(at(section, "attributes")) {
					label, value := fragments(at(attribute, "label")), fragments(at(attribute, "granularizedValue"))
					id := text(at(attribute, "label", "id"))
					if id == "" {
						id = label
					}
					if label != "" && value != "" && !seen[id] {
						p.Specs = append(p.Specs, Spec{Label: label, Value: value})
						seen[id] = true
					}
				}
			}
		}
	}
	if images := at(e, "productImages", "entity", "images"); images != nil {
		p.Images = []string{}
		for _, image := range array(images) {
			resolution := at(image, "hiRes")
			if resolution == nil {
				resolution = at(image, "lowRes")
			}
			id, ext := text(at(resolution, "physicalId")), text(at(resolution, "extension"))
			if id != "" && ext != "" {
				p.Images = append(p.Images, "https://m.media-amazon.com/images/I/"+id+"."+ext)
			}
		}
	}
	return p
}

func Parse(body []byte, requested []string) ([]Product, error) {
	var root object
	if err := json.Unmarshal(body, &root); err != nil || root == nil {
		return nil, fault.New("invalid_response", "invalid product JSON")
	}
	nodes := array(root["entities"])
	if nodes == nil {
		if text(root["type"]) != "product/v2" && text(root["type"]) != "error/v1" {
			return nil, fault.New("invalid_response", "invalid product envelope")
		}
		nodes = []any{root}
	}
	indexed := map[string]object{}
	for _, v := range nodes {
		node := obj(v)
		asin := text(at(node, "entity", "asin"))
		if asin == "" {
			path := text(at(node, "resource", "url"))
			if strings.HasPrefix(path, aapi.ProductPath) {
				asin = strings.TrimPrefix(path, aapi.ProductPath)
			}
		}
		if aapi.ValidASIN(asin) {
			if _, exists := indexed[asin]; !exists {
				indexed[asin] = node
			}
		}
	}
	result := make([]Product, 0, len(requested))
	for _, asin := range Dedupe(requested) {
		if node, ok := indexed[asin]; ok {
			result = append(result, parse(node, asin))
		} else {
			p := Bare(asin)
			p.Error = &ItemError{Code: "not_found"}
			result = append(result, p)
		}
	}
	return result, nil
}
