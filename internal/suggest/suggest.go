package suggest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/yashiels/amazon-za-cli/internal/aapi"
	"github.com/yashiels/amazon-za-cli/internal/fault"
)

func Parse(body []byte) ([]string, error) {
	var response struct {
		Suggestions []struct {
			Value string `json:"value"`
		} `json:"suggestions"`
	}
	if err := json.Unmarshal(body, &response); err != nil || response.Suggestions == nil {
		return nil, fault.New("invalid_response", "invalid suggestions")
	}
	values := make([]string, 0, len(response.Suggestions))
	for _, s := range response.Suggestions {
		if s.Value != "" {
			values = append(values, s.Value)
		}
	}
	return values, nil
}

func Get(ctx context.Context, t *aapi.Transport, prefix string) ([]string, error) {
	query := url.Values{"mid": {aapi.Marketplace}, "alias": {"aps"}, "prefix": {prefix}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://completion.amazon.co.za/api/2017/suggestions?"+query.Encode(), nil)
	if err != nil {
		return nil, fault.Usage()
	}
	req.Header.Set("User-Agent", aapi.UserAgent)
	response, err := t.Send(req)
	if err != nil {
		return nil, err
	}
	return Parse(response.Body)
}
