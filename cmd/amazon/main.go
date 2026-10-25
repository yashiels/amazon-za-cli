package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/yashiels/amazon-za-cli/internal/aapi"
	"github.com/yashiels/amazon-za-cli/internal/fault"
	"github.com/yashiels/amazon-za-cli/internal/product"
	"github.com/yashiels/amazon-za-cli/internal/session"
	"github.com/yashiels/amazon-za-cli/internal/suggest"
	"github.com/yashiels/amazon-za-cli/internal/watch"
	"github.com/yashiels/amazon-za-cli/internal/web"
)

var version = "dev"

type app struct {
	json                   bool
	maxRequests            int
	out                    io.Writer
	sessionPath, watchPath string
	transportFactory       func(int) *aapi.Transport
	transport              *aapi.Transport
	client                 *aapi.Client
	anonymous              session.Session
}

type searchResult struct {
	Query    string            `json:"query"`
	Page     int               `json:"page"`
	Items    []product.Product `json:"items"`
	Warnings []product.Warning `json:"warnings"`
}

func paths() (string, string) {
	home, _ := os.UserHomeDir()
	cache := os.Getenv("XDG_CACHE_HOME")
	if cache == "" {
		cache = filepath.Join(home, ".cache")
	}
	config := os.Getenv("XDG_CONFIG_HOME")
	if config == "" {
		config = filepath.Join(home, ".config")
	}
	return filepath.Join(cache, "amazon-za", "session.json"), filepath.Join(config, "amazon-za", "watch.json")
}

func (a *app) start(ctx context.Context, operations int, needsSession, force bool) error {
	if a.transport != nil {
		return nil
	}
	cached, valid := session.Session{}, false
	if needsSession && !force {
		var err error
		cached, valid, err = session.Cached(a.sessionPath, time.Now())
		if err != nil {
			return err
		}
	}
	cap := operations + 1
	if needsSession && !valid {
		cap++
	}
	if a.maxRequests > 0 && a.maxRequests < cap {
		cap = a.maxRequests
	}
	a.transport = a.transportFactory(cap)
	if needsSession && !valid {
		var err error
		cached, err = session.Bootstrap(ctx, a.transport, a.sessionPath, time.Now())
		if err != nil {
			return err
		}
	}
	a.anonymous = cached
	a.client = &aapi.Client{Transport: a.transport, SessionID: cached.ID, UBID: cached.UBID}
	return nil
}

func (a *app) fetch(ctx context.Context, asins []string, batch bool) ([]product.Product, error) {
	result := make([]product.Product, 0, len(asins))
	for offset := 0; offset < len(asins); offset += 20 {
		end := offset + 20
		if end > len(asins) {
			end = len(asins)
		}
		body, err := a.client.Products(ctx, asins[offset:end], batch)
		if err != nil {
			return nil, err
		}
		items, err := product.Parse(body, asins[offset:end])
		if err != nil {
			return nil, err
		}
		result = append(result, items...)
	}
	return result, nil
}

func compact(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > limit {
		return string(r[:limit-1]) + "…"
	}
	return s
}

func printProducts(out io.Writer, items []product.Product) error {
	w := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	for _, p := range items {
		title, price := "(unavailable)", "-"
		if p.Title != nil {
			title = *p.Title
		}
		if p.Price != nil {
			price = fmt.Sprintf("%s %.2f", p.Price.Currency, p.Price.Amount)
		}
		if p.Error != nil {
			title = p.Error.Code
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\n", p.ASIN, price, compact(title, 60)); err != nil {
			return err
		}
	}
	return w.Flush()
}

func printWatches(out io.Writer, entries []watch.Entry) error {
	w := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	for _, entry := range entries {
		target := "-"
		if entry.Target != nil {
			target = fmt.Sprintf("%.2f", *entry.Target)
		}
		if _, err := fmt.Fprintf(w, "%s\tbaseline %.2f\tlowest %.2f\ttarget %s\n", entry.ASIN, entry.Baseline.Amount, entry.LowestSeen.Amount, target); err != nil {
			return err
		}
	}
	return w.Flush()
}

func printProduct(out io.Writer, p product.Product) error {
	if err := printProducts(out, []product.Product{p}); err != nil {
		return err
	}
	values := [][2]string{{"URL", p.URL}}
	if p.Brand != nil {
		values = append(values, [2]string{"Brand", *p.Brand})
	}
	if p.Availability != nil {
		values = append(values, [2]string{"Availability", p.Availability.Message})
	}
	if p.Rating != nil {
		values = append(values, [2]string{"Rating", fmt.Sprintf("%.1f/5 (%d ratings)", p.Rating.Value, p.Rating.Count)})
	}
	if p.Delivery != nil {
		if p.Delivery.Fastest != nil {
			values = append(values, [2]string{"Fastest", *p.Delivery.Fastest})
		}
		if p.Delivery.Standard != nil {
			values = append(values, [2]string{"Standard", *p.Delivery.Standard})
		}
	}
	for _, spec := range p.Specs {
		values = append(values, [2]string{spec.Label, spec.Value})
	}
	for _, warning := range p.Warnings {
		values = append(values, [2]string{"Warning", warning.Field + " " + warning.Code})
	}
	w := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	for _, pair := range values {
		if _, err := fmt.Fprintf(w, "%s\t%s\n", compact(pair[0], 24), compact(pair[1], 70)); err != nil {
			return err
		}
	}
	return w.Flush()
}

func (a *app) emit(value any) error {
	if a.json {
		return json.NewEncoder(a.out).Encode(value)
	}
	switch v := value.(type) {
	case product.Product:
		return printProduct(a.out, v)
	case []product.Product:
		return printProducts(a.out, v)
	case searchResult:
		if err := printProducts(a.out, v.Items); err != nil {
			return err
		}
		for _, w := range v.Warnings {
			if _, err := fmt.Fprintln(a.out, w.Code); err != nil {
				return err
			}
		}
		return nil
	case []string:
		for _, s := range v {
			if _, err := fmt.Fprintln(a.out, s); err != nil {
				return err
			}
		}
		return nil
	case watch.Entry:
		return printWatches(a.out, []watch.Entry{v})
	case []watch.Entry:
		return printWatches(a.out, v)
	case []watch.Result:
		w := tabwriter.NewWriter(a.out, 0, 2, 2, ' ', 0)
		for _, result := range v {
			current, drop := "unavailable", "-"
			if result.Current != nil {
				current = fmt.Sprintf("%.2f", result.Current.Amount)
			}
			if result.Drop != nil {
				drop = fmt.Sprintf("%.2f", *result.Drop)
			}
			if _, err := fmt.Fprintf(w, "%s\tcurrent %s\tbaseline %.2f\tlowest %.2f\tdrop %s\ttarget %t\n", result.ASIN, current, result.Baseline.Amount, result.LowestSeen.Amount, drop, result.HitTarget); err != nil {
				return err
			}
		}
		return w.Flush()
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		w := tabwriter.NewWriter(a.out, 0, 2, 2, ' ', 0)
		for _, key := range keys {
			if _, err := fmt.Fprintf(w, "%s\t%s\n", compact(key, 24), compact(fmt.Sprint(v[key]), 70)); err != nil {
				return err
			}
		}
		return w.Flush()
	case map[string]string:
		values := map[string]any{}
		for key, value := range v {
			values[key] = value
		}
		return a.emit(values)
	default:
		b, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(a.out, string(b))
		return err
	}
}

func arguments(min, max int) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) < min || len(args) > max {
			return fault.Usage()
		}
		for _, arg := range args {
			if strings.TrimSpace(arg) == "" {
				return fault.Usage()
			}
		}
		return nil
	}
}

func parseASINs(inputs []string) ([]string, error) {
	asins := make([]string, 0, len(inputs))
	for _, input := range inputs {
		asin, err := product.ASIN(input)
		if err != nil {
			return nil, err
		}
		asins = append(asins, asin)
	}
	return product.Dedupe(asins), nil
}

func newCommand(a *app) *cobra.Command {
	showVersion := false
	root := &cobra.Command{Use: "amazon", Short: "Read-only amazon.co.za product search and prices", SilenceErrors: true, SilenceUsage: true, Args: arguments(0, 0)}
	root.SetOut(a.out)
	root.SetErr(io.Discard)
	root.PersistentFlags().BoolVar(&a.json, "json", false, "Output JSON")
	root.PersistentFlags().IntVar(&a.maxRequests, "max-requests", 0, "Lower the per-command request attempt cap")
	root.Flags().BoolVar(&showVersion, "version", false, "Print version")
	root.SetFlagErrorFunc(func(_ *cobra.Command, _ error) error { return fault.Usage() })
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if a.maxRequests < 0 || cmd.Flags().Changed("max-requests") && a.maxRequests == 0 {
			return fault.Usage()
		}
		return nil
	}
	root.RunE = func(cmd *cobra.Command, _ []string) error {
		if showVersion {
			if a.json {
				return a.emit(map[string]string{"version": version})
			}
			_, err := fmt.Fprintln(a.out, version)
			return err
		}
		return cmd.Help()
	}
	originalHelp := root.HelpFunc()
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		if a.json {
			_ = a.emit(map[string]string{"help": cmd.UsageString()})
			return
		}
		originalHelp(cmd, args)
	})
	limit, page := 10, 1
	search := &cobra.Command{Use: "search <query>", Short: "Search ranked products", Args: arguments(1, 1)}
	search.Flags().IntVar(&limit, "limit", 10, "Result count (1..48)")
	search.Flags().IntVar(&page, "page", 1, "Result page (1..20)")
	search.RunE = func(cmd *cobra.Command, args []string) error {
		if limit < 1 || limit > 48 || page < 1 || page > 20 {
			return fault.Usage()
		}
		if err := a.start(cmd.Context(), 1+(limit+19)/20, true, false); err != nil {
			return err
		}
		asins, err := web.Search(cmd.Context(), a.transport, args[0], limit, page)
		if err != nil {
			return err
		}
		items, err := a.fetch(cmd.Context(), asins, true)
		warnings := []product.Warning{}
		if err != nil {
			var edge *fault.Error
			if errors.As(err, &edge) && edge.Code == "edge_blocked" {
				return err
			}
			items = make([]product.Product, 0, len(asins))
			for _, asin := range asins {
				items = append(items, product.Bare(asin))
			}
			warnings = append(warnings, product.Warning{Code: "hydrate_failed"})
		}
		return a.emit(searchResult{Query: args[0], Page: page, Items: items, Warnings: warnings})
	}
	info := &cobra.Command{Use: "info <asin|url>", Short: "Inspect a product", Args: arguments(1, 1)}
	info.RunE = func(cmd *cobra.Command, args []string) error {
		asins, err := parseASINs(args)
		if err != nil {
			return err
		}
		if err := a.start(cmd.Context(), 1, true, false); err != nil {
			return err
		}
		items, err := a.fetch(cmd.Context(), asins, false)
		if err != nil {
			return err
		}
		if items[0].Error != nil {
			return &fault.Error{Message: "product unavailable", Code: "not_found", Status: items[0].Error.Status, Exit: 1}
		}
		return a.emit(items[0])
	}
	compare := &cobra.Command{Use: "compare <asin|url>...", Short: "Compare 2..20 products in one batch", Args: arguments(2, 20)}
	compare.RunE = func(cmd *cobra.Command, args []string) error {
		asins, err := parseASINs(args)
		if err != nil {
			return err
		}
		if len(asins) < 2 {
			return fault.Usage()
		}
		if err := a.start(cmd.Context(), 1, true, false); err != nil {
			return err
		}
		items, err := a.fetch(cmd.Context(), asins, true)
		if err != nil {
			return err
		}
		return a.emit(items)
	}
	suggestions := &cobra.Command{Use: "suggest <prefix>", Short: "Get query completions", Args: arguments(1, 1)}
	suggestions.RunE = func(cmd *cobra.Command, args []string) error {
		if err := a.start(cmd.Context(), 1, false, false); err != nil {
			return err
		}
		values, err := suggest.Get(cmd.Context(), a.transport, args[0])
		if err != nil {
			return err
		}
		return a.emit(values)
	}
	doctor := &cobra.Command{Use: "doctor", Short: "Refresh session and probe pinned app signing", Args: arguments(0, 0)}
	doctor.RunE = func(cmd *cobra.Command, _ []string) error {
		if err := a.start(cmd.Context(), 1, true, true); err != nil {
			return err
		}
		items, err := a.fetch(cmd.Context(), []string{"B0H471LF7H"}, false)
		if err != nil {
			return err
		}
		if items[0].Error != nil {
			return fault.New("aapi_rejected", "probe failed")
		}
		return a.emit(map[string]any{"appVersion": aapi.AppVersion, "appBuild": aapi.AppBuild, "sessionPresent": true, "sessionAgeSeconds": int(time.Since(a.anonymous.CreatedAt).Seconds()), "probe": "ok", "attempts": a.transport.Attempts()})
	}
	root.AddCommand(search, info, compare, suggestions, doctor, a.watchCommand())
	root.CompletionOptions.DisableDefaultCmd = true
	return root
}

func (a *app) watchCommand() *cobra.Command {
	store := watch.Store{Path: a.watchPath}
	root := &cobra.Command{Use: "watch", Short: "Manage local price watches", Args: arguments(0, 0), RunE: func(_ *cobra.Command, _ []string) error { return fault.Usage() }}
	target := 0.0
	add := &cobra.Command{Use: "add <asin|url>", Short: "Store current price as baseline", Args: arguments(1, 1)}
	add.Flags().Float64Var(&target, "target", 0, "Target price in ZAR")
	add.RunE = func(cmd *cobra.Command, args []string) error {
		asins, err := parseASINs(args)
		if err != nil {
			return err
		}
		var targetValue *float64
		if cmd.Flags().Changed("target") {
			if target < 0 || math.IsNaN(target) || math.IsInf(target, 0) {
				return fault.Usage()
			}
			targetValue = &target
		}
		if err := a.start(cmd.Context(), 1, true, false); err != nil {
			return err
		}
		items, err := a.fetch(cmd.Context(), asins, false)
		if err != nil {
			return err
		}
		entry, err := store.Add(asins[0], items[0].Price, targetValue, time.Now())
		if err != nil {
			return err
		}
		return a.emit(entry)
	}
	remove := &cobra.Command{Use: "rm <asin|url>", Short: "Remove a local watch", Args: arguments(1, 1)}
	remove.RunE = func(_ *cobra.Command, args []string) error {
		asins, err := parseASINs(args)
		if err != nil {
			return err
		}
		if err := store.Remove(asins[0]); err != nil {
			return err
		}
		return a.emit(map[string]string{"removed": asins[0]})
	}
	list := &cobra.Command{Use: "list", Short: "List local watches without network access", Args: arguments(0, 0)}
	list.RunE = func(_ *cobra.Command, _ []string) error {
		entries, err := store.List()
		if err != nil {
			return err
		}
		return a.emit(entries)
	}
	check := &cobra.Command{Use: "check", Short: "Reprice watches and report drops", Args: arguments(0, 0)}
	check.RunE = func(cmd *cobra.Command, _ []string) error {
		results, err := store.Check(func(asins []string) ([]product.Product, error) {
			if len(asins) == 0 {
				return []product.Product{}, nil
			}
			if err := a.start(cmd.Context(), (len(asins)+19)/20, true, false); err != nil {
				return nil, err
			}
			return a.fetch(cmd.Context(), asins, true)
		}, time.Now())
		if err != nil {
			return err
		}
		return a.emit(results)
	}
	root.AddCommand(add, remove, list, check)
	return root
}

func run(ctx context.Context, args []string, out, errOut io.Writer) int {
	sessionPath, watchPath := paths()
	a := &app{out: out, sessionPath: sessionPath, watchPath: watchPath, transportFactory: aapi.NewTransport}
	root := newCommand(a)
	root.SetArgs(args)
	if err := root.ExecuteContext(ctx); err != nil {
		public := fault.Public(err)
		for _, arg := range args {
			if arg == "--json" || arg == "--json=true" {
				a.json = true
			}
			if arg == "--json=false" {
				a.json = false
			}
		}
		if a.json {
			_ = json.NewEncoder(out).Encode(public)
		} else {
			_, _ = fmt.Fprintln(errOut, public.Message)
		}
		return public.Exit
	}
	return 0
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
