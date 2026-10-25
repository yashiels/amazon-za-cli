package aapi

import (
	"context"
	"net/http"
	"strings"
	"time"
)

const inclusions = "title(product.offer.title/v1),buyingOptions[].price(product.price/v1),buyingOptions[].availability(product.availability/v2),customerReviewsSummary(product.customer-reviews-summary/v1),byLine(product.by-line/v2),productImages(product.product-images/v2),productOverview(product.product-overview/v2),productCategory(product.offer.product-category/v1),buyingOptions[].delivery(product.delivery/v1)"

type Client struct {
	Transport       *Transport
	SessionID, UBID string
}

func (c *Client) Products(ctx context.Context, asins []string, batch bool) ([]byte, error) {
	typ := "product/v2"
	if batch {
		typ = "collection(product/v2)/v1"
	}
	path := ProductPath + strings.Join(asins, ",")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://data.amazon.co.za"+path, nil)
	if err != nil {
		return nil, err
	}
	req.URL.RawPath = path
	headers := []Header{{"Accept", `application/vnd.com.amazon.api+json; type="` + typ + `"; expand="` + inclusions + `"`}, {"Accept-Language", "en-ZA"}, {"X-Amzn-Session-Id", c.SessionID}, {"X-Amzn-UBID", c.UBID}}
	signature, err := Sign(http.MethodGet, path, "", headers, time.Now(), AppKey())
	if err != nil {
		return nil, err
	}
	for _, h := range headers {
		req.Header.Set(h.Name, h.Value)
	}
	req.Header.Set("X-AAPI-Signature", signature)
	req.Header.Set("User-Agent", UserAgent)
	result, err := c.Transport.Send(req)
	return result.Body, err
}
