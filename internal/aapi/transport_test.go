package aapi

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yashiels/amazon-za-cli/internal/fault"
)

func testTransport(t *testing.T, cap int, handler http.HandlerFunc) *Transport {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	tr := NewTransport(cap)
	rt := tr.client.Transport.(*http.Transport)
	rt.Proxy = nil
	rt.TLSClientConfig = rt.TLSClientConfig.Clone()
	rt.TLSClientConfig.InsecureSkipVerify = true
	rt.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	t.Cleanup(rt.CloseIdleConnections)
	tr.interval = 0
	return tr
}

func request(t *testing.T, raw string) *http.Request {
	t.Helper()
	r, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func code(err error) string {
	var e *fault.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestBatchWirePathAndHeaders(t *testing.T) {
	path := ProductPath + "B0H471LF7H,B0H4727J8L"
	transport := testTransport(t, 2, func(w http.ResponseWriter, r *http.Request) {
		if r.RequestURI != path || r.URL.EscapedPath() != path {
			t.Errorf("wire path changed: %s", r.RequestURI)
		}
		if r.Header.Get("Cookie") != "" || r.Header.Get("Accept-Encoding") != "" || r.Header.Get("Connection") != "" {
			t.Error("unsigned headers leaked")
		}
		if r.Header.Get("User-Agent") != UserAgent {
			t.Error("wrong app UA")
		}
		signed := []Header{}
		for _, name := range []string{"Accept", "Accept-Language", "X-Amzn-Session-Id", "X-Amzn-UBID"} {
			signed = append(signed, Header{name, r.Header.Get(name)})
		}
		stamp := strings.Split(strings.Split(r.Header.Get("X-AAPI-Signature"), "Time=")[1], " ")[0]
		at, err := time.Parse("2006-01-02T15:04:05-0700", stamp)
		if err != nil {
			t.Error(err)
		}
		expected, err := Sign("GET", path, "", signed, at, AppKey())
		if err != nil || expected != r.Header.Get("X-AAPI-Signature") {
			t.Error("wire signature mismatch")
		}
		_, _ = w.Write([]byte(`{"entities":[]}`))
	})
	client := Client{Transport: transport, SessionID: "synthetic-session", UBID: "synthetic-ubid"}
	if _, err := client.Products(context.Background(), []string{"B0H471LF7H", "B0H4727J8L"}, true); err != nil {
		t.Fatal(err)
	}
}

func TestTransportAllowlist(t *testing.T) {
	valid := []string{"https://www.amazon.co.za/", "https://www.amazon.co.za/s?k=ssd&page=1", "https://data.amazon.co.za" + ProductPath + "B0H471LF7H,B0H4727J8L", "https://completion.amazon.co.za/api/2017/suggestions?mid=AE08WJ6YKNBMC&alias=aps&prefix=ssd"}
	for _, raw := range valid {
		if err := Validate(request(t, raw)); err != nil {
			t.Errorf("refused %s", raw)
		}
	}
	invalid := []string{"http://www.amazon.co.za/", "https://amazon.co.za/", "https://www.amazon.co.za:443/", "https://u:p@www.amazon.co.za/", "https://www.amazon.co.za/#fragment", "https://www.amazon.co.za/cart", "https://www.amazon.co.za/s/", "https://www.amazon.co.za/%73", "https://www.amazon.co.za/?k=x", "https://www.amazon.co.za/s?bad=x", "https://www.amazon.co.za/s?k=a&k=b", "https://data.amazon.co.za" + ProductPath + "B0H471LF7H%2CB0H4727J8L", "https://data.amazon.co.za" + ProductPath + "%2e%2e", "https://data.amazon.co.za" + ProductPath + "B0H471LF7H%2f", "https://data.amazon.co.za" + ProductPath + "B0H471LF7H/price", "https://data.amazon.co.za" + ProductPath + "B0H471LF7H?x=1", "https://completion.amazon.co.za/api/2017/suggestions/"}
	for _, raw := range invalid {
		if err := Validate(request(t, raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	req := request(t, valid[0])
	req.Method = http.MethodPost
	if Validate(req) == nil {
		t.Fatal("POST accepted")
	}
}

func TestTransportAttemptCounts(t *testing.T) {
	cases := []struct {
		name      string
		statuses  []int
		cap, want int
		wantCode  string
	}{
		{"success", []int{200}, 3, 1, ""},
		{"retry_5xx", []int{503, 200}, 3, 2, ""},
		{"single_retry", []int{503, 503, 200}, 4, 2, "http_error"},
		{"budget_counts_retry", []int{503, 200}, 1, 1, "budget_exhausted"},
		{"forbidden", []int{403, 200}, 3, 1, "http_error"},
		{"rate_limit", []int{429, 200}, 3, 1, "http_error"},
		{"WAF", []int{202, 200}, 3, 1, "blocked_by_waf"},
		{"redirect", []int{302, 200}, 3, 1, "redirect_refused"},
		{"empty_404", []int{404, 200}, 3, 1, "aapi_rejected"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			tr := testTransport(t, tc.cap, func(w http.ResponseWriter, _ *http.Request) {
				i := calls
				if i >= len(tc.statuses) {
					i = len(tc.statuses) - 1
				}
				calls++
				w.Header().Set("Location", "https://www.amazon.co.za/")
				w.WriteHeader(tc.statuses[i])
			})
			_, err := tr.Send(request(t, "https://data.amazon.co.za"+ProductPath+"B0H471LF7H"))
			if calls != tc.want || tr.Attempts() != tc.want || code(err) != tc.wantCode {
				t.Fatalf("calls=%d attempts=%d code=%s", calls, tr.Attempts(), code(err))
			}
		})
	}
}

func TestCommandBudgetSharedAcrossOperations(t *testing.T) {
	cases := []struct {
		name                      string
		ops, bootstrap, cap, want int
	}{
		{"info_cold", 1, 1, 3, 3}, {"info_warm", 1, 0, 2, 2}, {"search_cold", 2, 1, 4, 4}, {"compare_cold", 1, 1, 3, 3}, {"watch_100_cold", 5, 1, 7, 7}, {"suggest", 1, 0, 2, 2}, {"lower_cap", 5, 1, 2, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			tr := testTransport(t, tc.cap, func(w http.ResponseWriter, _ *http.Request) {
				calls++
				if calls == 1 {
					w.WriteHeader(503)
				} else {
					w.WriteHeader(200)
				}
			})
			for i := 0; i < tc.ops+tc.bootstrap; i++ {
				_, err := tr.Send(request(t, "https://www.amazon.co.za/"))
				if err != nil {
					if code(err) != "budget_exhausted" {
						t.Fatal(err)
					}
					break
				}
			}
			if calls != tc.want {
				t.Fatalf("got %d want %d", calls, tc.want)
			}
		})
	}
}

func TestWAFBodyAndCaps(t *testing.T) {
	fixture, err := os.ReadFile("../../testdata/waf_challenge.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{string(fixture), "window.awsWafCoo = {}"} {
		tr := testTransport(t, 3, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) })
		_, err := tr.Send(request(t, "https://www.amazon.co.za/s?k=x"))
		if code(err) != "blocked_by_waf" || tr.Attempts() != 1 {
			t.Fatal("WAF marker missed")
		}
	}
	for _, tc := range []struct {
		host  string
		limit int
	}{{"www.amazon.co.za/", 4 << 20}, {"completion.amazon.co.za/api/2017/suggestions", 2 << 20}} {
		tr := testTransport(t, 3, func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, strings.Repeat("x", tc.limit+1)) })
		_, err := tr.Send(request(t, "https://"+tc.host))
		if code(err) != "response_too_large" || tr.Attempts() != 1 {
			t.Fatal("body cap missed")
		}
	}
}

func TestTimeoutAndNetworkRetry(t *testing.T) {
	var calls atomic.Int32
	tr := newTransport(3, roundTripFunc(func(*http.Request) (*http.Response, error) { calls.Add(1); return nil, &net.DNSError{IsTimeout: true} }), 0)
	_, err := tr.Send(request(t, "https://www.amazon.co.za/"))
	if calls.Load() != 2 || code(err) != "network_error" {
		t.Fatal("network retry count incorrect")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRetryAllowanceIsSharedAcrossSends(t *testing.T) {
	calls := 0
	tr := testTransport(t, 5, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 2 {
			w.WriteHeader(200)
		} else {
			w.WriteHeader(503)
		}
	})
	req := request(t, "https://www.amazon.co.za/")
	if _, err := tr.Send(req); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Send(req); code(err) != "http_error" {
		t.Fatal("second operation should fail without another retry")
	}
	if calls != 3 {
		t.Fatalf("got %d attempts, want 3", calls)
	}
}

func TestBroken4xxBodyIsNotRetried(t *testing.T) {
	calls := 0
	tr := newTransport(3, roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 404, Header: http.Header{}, Body: brokenBody{}}, nil
	}), 0)
	if _, err := tr.Send(request(t, "https://www.amazon.co.za/")); err == nil {
		t.Fatal("broken body accepted")
	}
	if calls != 1 {
		t.Fatal("retried a 4xx body failure")
	}
}

type brokenBody struct{}

func (brokenBody) Read([]byte) (int, error) { return 0, errors.New("body interrupted") }
func (brokenBody) Close() error             { return nil }

func TestProductionTransportRequiresTLS13(t *testing.T) {
	transport := NewTransport(3)
	rt, ok := transport.client.Transport.(*http.Transport)
	if !ok || rt.TLSClientConfig == nil || rt.TLSClientConfig.MinVersion != tls.VersionTLS13 {
		t.Fatal("production transport must require TLS 1.3")
	}
}

func TestEdge503IsNotRetried(t *testing.T) {
	body, err := os.ReadFile("../../testdata/edge_503.html")
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 517 || strings.Count(string(body), "\r\n") != 11 {
		t.Fatal("edge fixture must preserve exact CRLF body")
	}
	for _, host := range []string{"www.amazon.co.za/", "data.amazon.co.za" + ProductPath + "B0H471LF7H", "completion.amazon.co.za/api/2017/suggestions"} {
		t.Run(host, func(t *testing.T) {
			calls := 0
			tr := testTransport(t, 3, func(w http.ResponseWriter, _ *http.Request) { calls++; w.WriteHeader(503); _, _ = w.Write(body) })
			_, err := tr.Send(request(t, "https://"+host))
			public := fault.Public(err)
			if public.Code != "edge_blocked" || public.Status != 503 || public.Exit != 1 || calls != 1 || tr.Attempts() != 1 {
				t.Fatalf("edge classification: %+v calls=%d", public, calls)
			}
		})
	}
}
