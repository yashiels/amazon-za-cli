package aapi

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/yashiels/amazon-za-cli/internal/fault"
)

var asinPattern = regexp.MustCompile(`^[A-Z0-9]{10}$`)

func ValidASIN(s string) bool { return asinPattern.MatchString(s) }

type Response struct {
	Body   []byte
	Header http.Header
	Status int
}
type Transport struct {
	client        *http.Client
	cap, attempts int
	retried       bool
	last          time.Time
	interval      time.Duration
}

func NewTransport(cap int) *Transport {
	rt := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS13},
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		DisableCompression:    true,
		MaxIdleConnsPerHost:   -1,
	}
	return newTransport(cap, rt, time.Second)
}

func newTransport(cap int, rt http.RoundTripper, interval time.Duration) *Transport {
	return &Transport{cap: cap, interval: interval, client: &http.Client{
		Transport: rt, Timeout: 20 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (t *Transport) Attempts() int { return t.attempts }

func Validate(req *http.Request) error {
	u := req.URL
	bad := func() error { return fault.New("unsafe_request", "request refused") }
	if req.Method != http.MethodGet || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" || u.Opaque != "" || req.Host != "" && req.Host != u.Host {
		return bad()
	}
	raw := u.EscapedPath()
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return bad()
	}
	allowedQuery := map[string]bool{}
	switch u.Host {
	case "www.amazon.co.za":
		if raw != "/" && raw != "/s" {
			return bad()
		}
		if raw == "/s" {
			allowedQuery = map[string]bool{"k": true, "page": true}
		}
	case "data.amazon.co.za":
		if !strings.HasPrefix(raw, ProductPath) {
			return bad()
		}
		asins := strings.Split(strings.TrimPrefix(raw, ProductPath), ",")
		if len(asins) > 20 {
			return bad()
		}
		for _, asin := range asins {
			if !ValidASIN(asin) {
				return bad()
			}
		}
	case "completion.amazon.co.za":
		if raw != "/api/2017/suggestions" {
			return bad()
		}
		allowedQuery = map[string]bool{"mid": true, "alias": true, "prefix": true}
	default:
		return bad()
	}
	for k, values := range query {
		if !allowedQuery[k] || len(values) != 1 {
			return bad()
		}
	}
	return nil
}

func IsWAF(status int, body []byte) bool {
	s := string(body)
	return status == 202 || strings.Contains(s, "awsWafCookieDomainList") || strings.Contains(s, "window.awsWafCoo") || strings.Contains(s, "triggerInterstitialChallenge") || strings.Contains(s, "bm-verify=")
}

func pause(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fault.New("network_error", "request canceled")
	case <-timer.C:
		return nil
	}
}

func (t *Transport) Send(req *http.Request) (Response, error) {
	if err := Validate(req); err != nil {
		return Response{}, err
	}
	for {
		if t.attempts >= t.cap {
			return Response{}, fault.New("budget_exhausted", "attempt limit")
		}
		if err := pause(req.Context(), time.Until(t.last.Add(t.interval))); err != nil {
			return Response{}, err
		}
		t.attempts++
		t.last = time.Now()
		resp, err := t.client.Do(req.Clone(req.Context()))
		result := Response{}
		if err == nil {
			limit := int64(2 << 20)
			if req.URL.Host == "www.amazon.co.za" {
				limit = 4 << 20
			}
			result.Status, result.Header = resp.StatusCode, resp.Header
			result.Body, err = io.ReadAll(io.LimitReader(resp.Body, limit+1))
			_ = resp.Body.Close()
			if int64(len(result.Body)) > limit {
				return Response{}, fault.New("response_too_large", "body cap")
			}
			if result.Status == http.StatusServiceUnavailable && strings.Contains(string(result.Body), "Website Temporarily Unavailable") {
				return result, &fault.Error{Message: "Amazon edge blocked this request", Code: "edge_blocked", Status: result.Status, Exit: 1}
			}
			if IsWAF(result.Status, result.Body) {
				return result, fault.New("blocked_by_waf", "WAF")
			}
			if result.Status >= 300 && result.Status < 400 {
				return result, &fault.Error{Message: "redirect refused", Code: "redirect_refused", Status: result.Status, Exit: 1}
			}
		}
		if result.Status == 403 || result.Status == 429 {
			return result, &fault.Error{Message: "request rejected", Code: "http_error", Status: result.Status, Exit: 1}
		}
		retryable := err != nil && (result.Status < 400 || result.Status >= 500) || result.Status >= 500
		if retryable && !t.retried && req.Context().Err() == nil {
			t.retried = true
			var jitter [1]byte
			_, _ = rand.Read(jitter[:])
			if err := pause(req.Context(), time.Duration(100+int(jitter[0]))*time.Millisecond); err != nil {
				return result, err
			}
			continue
		}
		if err != nil {
			return Response{}, fault.New("network_error", "request failed")
		}
		if result.Status != 200 {
			code := "http_error"
			if req.URL.Host == "data.amazon.co.za" && result.Status == 404 && len(result.Body) == 0 {
				code = "aapi_rejected"
			}
			return result, &fault.Error{Message: "request rejected", Code: code, Status: result.Status, Exit: 1}
		}
		return result, nil
	}
}

func NewTransportWithRoundTripper(cap int, rt http.RoundTripper) *Transport {
	return newTransport(cap, rt, 0)
}
