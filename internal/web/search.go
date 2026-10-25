package web

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/yashiels/amazon-za-cli/internal/aapi"
	"github.com/yashiels/amazon-za-cli/internal/fault"
	"golang.org/x/net/html"
)

func ParseSearch(body []byte, limit int) ([]string, error) {
	if limit < 1 || limit > 48 {
		return nil, fault.Usage()
	}
	if aapi.IsWAF(200, body) {
		return nil, fault.New("blocked_by_waf", "WAF")
	}
	result := make([]string, 0)
	seen := map[string]bool{}
	tokenizer := html.NewTokenizer(bytes.NewReader(body))
	for {
		kind := tokenizer.Next()
		if kind == html.ErrorToken {
			if tokenizer.Err() != io.EOF {
				return nil, fault.New("invalid_response", "invalid HTML")
			}
			return result, nil
		}
		if kind != html.StartTagToken && kind != html.SelfClosingTagToken {
			continue
		}
		token := tokenizer.Token()
		component, asin := "", ""
		for _, attr := range token.Attr {
			if attr.Key == "data-component-type" {
				component = attr.Val
			}
			if attr.Key == "data-asin" {
				asin = attr.Val
			}
		}
		if component == "s-search-result" && aapi.ValidASIN(asin) && !seen[asin] {
			seen[asin] = true
			result = append(result, asin)
			if len(result) == limit {
				return result, nil
			}
		}
	}
}

func Search(ctx context.Context, t *aapi.Transport, query string, limit, page int) ([]string, error) {
	if limit < 1 || limit > 48 || page < 1 || page > 20 {
		return nil, fault.Usage()
	}
	params := url.Values{"k": {query}, "page": {strconv.Itoa(page)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.amazon.co.za/s?"+params.Encode(), nil)
	if err != nil {
		return nil, fault.Usage()
	}
	req.Header.Set("User-Agent", aapi.UserAgent)
	response, err := t.Send(req)
	if err != nil {
		return nil, err
	}
	return ParseSearch(response.Body, limit)
}
