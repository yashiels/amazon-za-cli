package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yashiels/amazon-za-cli/internal/aapi"
	"github.com/yashiels/amazon-za-cli/internal/fault"
	"github.com/yashiels/amazon-za-cli/internal/persist"
	"github.com/yashiels/amazon-za-cli/internal/session"
	"github.com/yashiels/amazon-za-cli/internal/watch"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func harness(t *testing.T, handler http.HandlerFunc) (*app, *bytes.Buffer) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	base, _ := url.Parse(server.URL)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	a := &app{out: out, sessionPath: filepath.Join(dir, "cache", "session.json"), watchPath: filepath.Join(dir, "config", "watch.json")}
	a.transportFactory = func(cap int) *aapi.Transport {
		return aapi.NewTransportWithRoundTripper(cap, roundTrip(func(r *http.Request) (*http.Response, error) {
			copy := r.Clone(r.Context())
			copy.Host = r.URL.Host
			copy.URL.Host = base.Host
			return server.Client().Transport.RoundTrip(copy)
		}))
	}
	return a, out
}

func execute(t *testing.T, a *app, args ...string) error {
	t.Helper()
	cmd := newCommand(a)
	cmd.SetArgs(args)
	return cmd.ExecuteContext(context.Background())
}

func bootstrap(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: "session-id", Value: "test-session", MaxAge: 3600})
	http.SetCookie(w, &http.Cookie{Name: "ubid-acbza", Value: "test-ubid", MaxAge: 3600})
}

func TestCommandFixturesAndExactAttemptCounts(t *testing.T) {
	full, batch, search, completion := fixture(t, "product_full.json"), fixture(t, "batch_4_with_bogus.json"), fixture(t, "search_ssd_enclosure.html"), fixture(t, "suggest.json")
	for _, tc := range []struct {
		name string
		args []string
		want int
		warm bool
	}{
		{"info", []string{"info", "B0H471LF7H", "--json"}, 2, false},
		{"info_cached", []string{"info", "B0H471LF7H", "--json"}, 1, true},
		{"compare", []string{"compare", "B0H471LF7H", "B0H4727J8L", "--json"}, 2, false},
		{"search", []string{"search", "ssd enclosure", "--limit", "5", "--json"}, 3, false},
		{"suggest", []string{"suggest", "ssd encl", "--json"}, 1, false},
		{"doctor_forces_bootstrap", []string{"doctor", "--json"}, 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			a, out := harness(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "GET" {
					t.Error("non-GET")
				}
				switch r.Host {
				case "www.amazon.co.za":
					if r.URL.Path == "/" {
						bootstrap(w)
					} else {
						_, _ = w.Write(search)
					}
				case "data.amazon.co.za":
					if r.Header.Get("Cookie") != "" {
						t.Error("data cookies")
					}
					if strings.Contains(r.URL.Path, ",") || strings.Contains(r.Header.Get("Accept"), "collection(") {
						_, _ = w.Write(batch)
					} else {
						_, _ = w.Write(full)
					}
				case "completion.amazon.co.za":
					_, _ = w.Write(completion)
				default:
					t.Error("unexpected host")
				}
			})
			if tc.warm {
				now := time.Now()
				if err := persist.Write(a.sessionPath, session.Session{ID: "cached-session", UBID: "cached-ubid", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
					t.Fatal(err)
				}
			}
			if err := execute(t, a, tc.args...); err != nil {
				t.Fatal(err)
			}
			if calls != tc.want || a.transport.Attempts() != tc.want {
				t.Fatalf("got %d calls want %d", calls, tc.want)
			}
			if !json.Valid(out.Bytes()) {
				t.Fatalf("non-JSON stdout: %s", out.String())
			}
			if tc.name == "info" && !strings.Contains(out.String(), `"amount":199`) {
				t.Fatal("fixture price missing")
			}
		})
	}
}

func TestSearchHydrationFallback(t *testing.T) {
	search := fixture(t, "search_ssd_enclosure.html")
	calls := 0
	a, out := harness(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Path {
		case "/":
			bootstrap(w)
		case "/s":
			_, _ = w.Write(search)
		default:
			w.WriteHeader(403)
		}
	})
	if err := execute(t, a, "search", "ssd enclosure", "--limit", "5", "--json"); err != nil {
		t.Fatal(err)
	}
	var result searchResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if calls != 3 || len(result.Items) != 5 || len(result.Warnings) != 1 || result.Warnings[0].Code != "hydrate_failed" {
		t.Fatal("hydration fallback failed")
	}
	for _, p := range result.Items {
		if p.Hydrated || p.Price != nil || p.Title != nil {
			t.Fatal("invented hydrated product")
		}
	}
}

func TestCommandBudgetAndSingleRetry(t *testing.T) {
	full := fixture(t, "product_full.json")
	for _, cap := range []int{1, 2, 3, 4} {
		t.Run(fmt.Sprint(cap), func(t *testing.T) {
			calls := 0
			a, _ := harness(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path == "/" {
					bootstrap(w)
					return
				}
				if calls == 2 {
					w.WriteHeader(503)
					return
				}
				_, _ = w.Write(full)
			})
			err := execute(t, a, "info", "B0H471LF7H", "--max-requests", fmt.Sprint(cap), "--json")
			want := cap
			if want > 3 {
				want = 3
			}
			if calls != want {
				t.Fatalf("got %d calls want %d", calls, want)
			}
			if cap < 3 {
				var e *fault.Error
				if !errors.As(err, &e) || e.Code != "budget_exhausted" {
					t.Fatal("cap not enforced")
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWatch100Batching(t *testing.T) {
	calls, dataCalls := 0, 0
	a, _ := harness(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Host == "www.amazon.co.za" {
			bootstrap(w)
			return
		}
		dataCalls++
		asins := strings.Split(strings.TrimPrefix(r.URL.Path, aapi.ProductPath), ",")
		if len(asins) != 20 {
			t.Error("batch size", len(asins))
		}
		_, _ = w.Write([]byte(`{"type":"collection(product/v2)/v1","entities":[]}`))
	})
	entries := make([]watch.Entry, 100)
	for i := range entries {
		entries[i] = watch.Entry{ASIN: fmt.Sprintf("B%09d", i), AddedAt: time.Now()}
		entries[i].Baseline.Currency = "ZAR"
		entries[i].LowestSeen.Currency = "ZAR"
	}
	if err := persist.Write(a.watchPath, entries); err != nil {
		t.Fatal(err)
	}
	if err := execute(t, a, "watch", "check", "--json"); err != nil {
		t.Fatal(err)
	}
	if calls != 6 || dataCalls != 5 {
		t.Fatalf("watch calls %d data %d", calls, dataCalls)
	}
}

func TestEmpty404NoSessionRefresh(t *testing.T) {
	empty := fixture(t, "empty_session_404.txt")
	if strings.TrimSpace(string(empty)) != "status=404" {
		t.Fatal("unexpected 404 fixture metadata")
	}
	calls := 0
	a, _ := harness(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/" {
			bootstrap(w)
			return
		}
		w.WriteHeader(404)

	})
	err := execute(t, a, "info", "B0H471LF7H", "--json")
	var e *fault.Error
	if !errors.As(err, &e) || e.Code != "aapi_rejected" || calls != 2 {
		t.Fatalf("404 handling %v calls %d", err, calls)
	}
}

func TestUsageJSONAndExitCodes(t *testing.T) {
	for _, args := range [][]string{
		{"unknown", "--json"}, {"info", "bad", "--json"}, {"compare", "B0H471LF7H", "--json"}, {"search", "x", "--limit", "49", "--json"}, {"search", "x", "--page", "21", "--json"}, {"suggest", "--json"}, {"info", "--bad", "--json"}, {"--max-requests", "-1", "--json"}, {"--max-requests", "0", "--json"},
	} {
		var out, errOut bytes.Buffer
		exit := run(context.Background(), args, &out, &errOut)
		if exit != 4 || !json.Valid(out.Bytes()) || errOut.Len() != 0 {
			t.Fatalf("args=%v exit=%d out=%s stderr=%s", args, exit, out.String(), errOut.String())
		}
		var e fault.Error
		if err := json.Unmarshal(out.Bytes(), &e); err != nil || e.Code != "usage" {
			t.Fatal("invalid envelope")
		}
	}
	for _, args := range [][]string{{"--version", "--json"}, {"--help", "--json"}, {"info", "--help", "--json"}} {
		var out, errOut bytes.Buffer
		if run(context.Background(), args, &out, &errOut) != 0 || !json.Valid(out.Bytes()) {
			t.Fatalf("JSON metadata %v: %s", args, out.String())
		}
	}
}

func TestErrorRedactionPlantedCredentials(t *testing.T) {
	planted := []string{"planted-signature", aapi.AppKey().ID, "planted-session", "planted-ubid", "planted-raw-body"}
	secretText := strings.Join(planted, " ")
	for _, err := range []error{errors.New(secretText), &url.Error{Op: "Get", URL: "https://www.amazon.co.za/?" + secretText, Err: errors.New(secretText)}, &fault.Error{Message: secretText, Code: "network_error", Status: 403, Exit: 1}} {
		b, marshalErr := json.Marshal(fault.Public(err))
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		for _, value := range planted {
			if strings.Contains(string(b), value) {
				t.Fatal("credential leaked")
			}
		}
	}
	a, _ := harness(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			bootstrap(w)
			return
		}
		w.WriteHeader(403)
		_, _ = w.Write([]byte(secretText))
	})
	err := execute(t, a, "info", "B0H471LF7H", "--json")
	b, _ := json.Marshal(fault.Public(err))
	for _, value := range planted {
		if strings.Contains(string(b), value) {
			t.Fatal("HTTP body leaked")
		}
	}
}

func TestSearch48UsesBatchesOf20(t *testing.T) {
	calls, dataCalls := 0, 0
	var html strings.Builder
	for i := 0; i < 48; i++ {
		fmt.Fprintf(&html, `<div data-component-type="s-search-result" data-asin="B%09d"></div>`, i)
	}
	a, out := harness(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/" {
			bootstrap(w)
			return
		}
		if r.URL.Path == "/s" {
			_, _ = fmt.Fprint(w, html.String())
			return
		}
		dataCalls++
		count := len(strings.Split(strings.TrimPrefix(r.URL.Path, aapi.ProductPath), ","))
		if count > 20 {
			t.Error("oversized batch")
		}
		_, _ = w.Write([]byte(`{"type":"collection(product/v2)/v1","entities":[]}`))
	})
	if err := execute(t, a, "search", "ssd enclosure", "--limit", "48", "--json"); err != nil {
		t.Fatal(err)
	}
	var result searchResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 48 || calls != 5 || dataCalls != 3 {
		t.Fatalf("items=%d calls=%d data=%d", len(result.Items), calls, dataCalls)
	}
}

func TestHumanInfoAndWatchOutput(t *testing.T) {
	full := fixture(t, "product_full.json")
	a, out := harness(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			bootstrap(w)
			return
		}
		_, _ = w.Write(full)
	})
	if err := execute(t, a, "info", "B0H471LF7H"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "In stock") || !strings.Contains(out.String(), "199.00") || !strings.Contains(out.String(), "Global Cellular") {
		t.Fatal("human details missing")
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if len([]rune(line)) > 100 {
			t.Fatal("human output exceeds 100 columns")
		}
	}
}

func TestWatchCommandLifecycle(t *testing.T) {
	full := fixture(t, "product_full.json")
	calls := 0
	a, out := harness(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/" {
			bootstrap(w)
			return
		}
		_, _ = w.Write(full)
	})
	for _, args := range [][]string{{"watch", "add", "B0H471LF7H", "--target", "200", "--json"}, {"watch", "list", "--json"}, {"watch", "check", "--json"}, {"watch", "rm", "B0H471LF7H", "--json"}, {"watch", "list", "--json"}} {
		a.transport = nil
		a.client = nil
		out.Reset()
		if err := execute(t, a, args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if !json.Valid(out.Bytes()) {
			t.Fatal("watch command returned invalid JSON")
		}
		if args[1] == "check" && !strings.Contains(out.String(), `"hitTarget":true`) {
			t.Fatal("watch target not reported")
		}
	}
	if calls != 3 || strings.TrimSpace(out.String()) != "[]" {
		t.Fatalf("watch lifecycle calls=%d output=%s", calls, out.String())
	}
}

func TestEdgeBlockPropagatesThroughCommands(t *testing.T) {
	edge, search := fixture(t, "edge_503.html"), fixture(t, "search_ssd_enclosure.html")
	for _, tc := range []struct {
		name             string
		args             []string
		bootstrapBlocked bool
		wantCalls        int
	}{
		{"bootstrap", []string{"info", "B0H471LF7H", "--json"}, true, 1},
		{"info", []string{"info", "B0H471LF7H", "--json"}, false, 2},
		{"search_hydration", []string{"search", "ssd enclosure", "--limit", "5", "--json"}, false, 3},
		{"suggest", []string{"suggest", "ssd encl", "--json"}, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			a, out := harness(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path == "/" && !tc.bootstrapBlocked {
					bootstrap(w)
					return
				}
				if r.URL.Path == "/s" {
					_, _ = w.Write(search)
					return
				}
				w.WriteHeader(503)
				_, _ = w.Write(edge)
			})
			err := execute(t, a, tc.args...)
			public := fault.Public(err)
			if public.Code != "edge_blocked" || public.Status != 503 || public.Exit != 1 || calls != tc.wantCalls {
				t.Fatalf("error=%+v calls=%d", public, calls)
			}
			if out.Len() != 0 {
				t.Fatal("edge-blocked command emitted a success payload")
			}
		})
	}
}

func TestUnknownFlagReturnsUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	exit := run(context.Background(), []string{"--unknown", "--json"}, &out, &errOut)
	var envelope fault.Error
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if exit != 4 || envelope.Code != "usage" || errOut.Len() != 0 {
		t.Fatalf("exit=%d code=%s stderr=%s", exit, envelope.Code, errOut.String())
	}
}

func TestStdoutFailuresReturnRedactedRuntimeError(t *testing.T) {
	for _, message := range []string{"stdout unavailable", "fake-signature=secret-value fake-session=private-value"} {
		t.Run(message, func(t *testing.T) {
			out := &failFirstWriter{err: errors.New(message)}
			var errOut bytes.Buffer
			exit := run(context.Background(), []string{"--version", "--json"}, out, &errOut)
			var envelope fault.Error
			if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if exit != 1 || envelope.Code != "runtime_error" || envelope.Message != "unexpected failure" || errOut.Len() != 0 {
				t.Fatalf("exit=%d envelope=%+v stderr=%s", exit, envelope, errOut.String())
			}
			if strings.Contains(out.String(), message) {
				t.Fatal("untyped failure text leaked")
			}
		})
	}
}

type failFirstWriter struct {
	bytes.Buffer
	err error
}

func (w *failFirstWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		err := w.err
		w.err = nil
		return 0, err
	}
	return w.Buffer.Write(p)
}
