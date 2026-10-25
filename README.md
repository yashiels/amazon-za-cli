# amazon-za-cli

Read-only amazon.co.za product search, prices, comparisons and local price watches. One Go binary named `amazon`, with JSON output for agents and compact terminal output.

Not affiliated with Amazon. Uses private, undocumented APIs reverse-engineered from the Android app.

## Install

```sh
brew install yashiels/tap/amazon
```

The Homebrew formula is published from the GitHub release by the tap workflow in [Release](#release). To build from source, use Go 1.23 or newer:

```sh
make build
./amazon --version
make install PREFIX="$HOME/.local"
```

## Commands

```sh
amazon search "ssd enclosure" --limit 5 --page 1 --json
amazon info B0H471LF7H --json
amazon info https://www.amazon.co.za/dp/B0H471LF7H --json
amazon compare B0H471LF7H B0H4727J8L --json
amazon suggest "ssd encl" --json
amazon watch add B0H471LF7H --target 180 --json
amazon watch list --json
amazon watch check --json
amazon watch rm B0H471LF7H --json
amazon doctor --json
```

Search defaults to 10 results, supports 1..48 results and pages 1..20, and deduplicates ASINs before applying the limit. Products are hydrated in batches of at most 20. Compare accepts 2..20 distinct products and uses one batch call. Product URLs may use `/dp/`, `/gp/product/`, `/gp/aw/d/` or a slug before `/dp/`. Only amazon.co.za product URLs over HTTPS are accepted. Short links cannot be followed.

Watchlists hold up to 100 products. Adding a watch fetches its current ZAR price and fixes the baseline. Re-adding resets that baseline. Checks report current price, baseline, lowest seen price, drop from baseline, and whether the optional target was met. Checks update only lowest seen price and last checked time. An unavailable price leaves those stored values unchanged. `watch list` and `watch rm` make no network requests.

Doctor establishes a fresh anonymous session and probes the pinned Android app signing configuration. It reports app version/build, session presence and age, attempt count, and probe result. It never prints session values or signing credentials.

## JSON contract

`--json` sends only JSON to stdout, including errors, help and version output. Exit codes are 0 for success, 1 for runtime/API failure, 3 reserved for authentication, and 4 for invalid arguments. Runtime errors use `{error,code,status?}` envelopes. Amazon can block anonymous traffic; `blocked_by_waf` or `edge_blocked` means stop without retrying.

`info` returns one product. `compare` returns an array in first input order. Batch items can carry `error:{code,status?}`; a missing entity uses `not_found`. Unknown or denied fields are null. Inclusion warnings carry the ASIN, field path, HTTP status and API code. A price is not inferred when it is absent.

This excerpt from the committed `product_full.json` fixture illustrates the product fields; it is a captured snapshot, not a current offer:

```json
{
  "asin": "B0H471LF7H",
  "brand": "Global Cellular",
  "price": {"amount": 199, "currency": "ZAR"},
  "listPrice": null,
  "availability": {"type": "IN_STOCK", "message": "In stock"},
  "delivery": {
    "fastest": "2026-10-25T18:00:00.000Z",
    "standard": "2026-10-26T18:00:00.000Z"
  },
  "rating": {
    "value": 4,
    "count": 7,
    "histogram": {"1": 18, "2": 0, "3": 0, "4": 29, "5": 53}
  },
  "hydrated": true,
  "warnings": []
}
```

Search returns `{query,page,items,warnings}`. If hydration fails, search still succeeds with ASINs and URLs, `hydrated:false`, null product fields, and a top-level `hydrate_failed` warning. Suggest returns strings; the first three values in the committed suggestion fixture are:

```json
["ssd enclosure", "ssd enclosure m.2", "ssd enclosure sata"]
```

Fields may be added within a 0.x minor version. Removals require a minor version bump and a changelog entry.

## Storage and request limits

Anonymous session cookies are read once from the storefront and sent as signed headers to the data API, never as cookies. The cache is `$XDG_CACHE_HOME/amazon-za/session.json` (default `~/.cache/amazon-za/session.json`), valid until the earliest relevant cookie expiry or seven days. Empty 404 responses from the API produce `aapi_rejected`; they do not trigger a session refresh.

Watches live at `$XDG_CONFIG_HOME/amazon-za/watch.json` (default `~/.config/amazon-za/watch.json`). Directories use 0700 and files 0600. Writes use a same-directory temporary file, fsync and rename. Symlinks at the file or any parent are refused. Watch updates use a file lock with a five-second acquisition timeout.

All Amazon traffic requires TLS 1.3 or newer and is HTTPS GET to three pinned hosts and exact allowed routes. Redirects are refused. Requests are spaced at least one second apart within a command. Connect/TLS timeouts are five seconds, response headers ten seconds and the whole request twenty seconds. Bodies are capped at 4 MiB for storefront HTML and 2 MiB for JSON.

One retry is shared across the entire command, only for network failures or 5xx responses. No retries follow 4xx, WAF challenges or a 503 edge page reporting `Website Temporarily Unavailable`. Attempt budgets include bootstrap when needed, operation requests, and one retry. Cold-cache defaults: info/compare 3, search 4 for up to 20 results, watch check `2 + ceil(items/20)`. Search with 21..48 results adds the required batch operations; suggest has a cap of 2. `--max-requests N` lowers these caps, including retries. Concurrent CLI processes each have their own request timing.

There are no login, cart, wishlist, checkout or order operations. Private APIs and app signing credentials can change; fixture success does not guarantee live access.

## Development

```sh
make test
make lint
make build
```

Tests use captured catalogue fixtures and local HTTP servers. The signer includes independent OpenSSL vectors, an accepted-live signature vector and a literal-comma wire test. When golangci-lint v2 is unavailable, `make lint` runs gofmt checks and `go vet`.

The approved specification is in [docs/plan.md](docs/plan.md). The bundled agent skill is [skill/SKILL.md](skill/SKILL.md).

## Release

Releases are manual. `ship.yml` runs only from `main` through `workflow_dispatch`. Push and tag events do not publish. The `version` input is the binary identity passed to the linker. The workflow does not edit or commit `version.env`.

A dry run is the default. It tests, builds the four static archives, and uploads them with `checksums.txt` as a workflow artifact. It does not create a tag or GitHub release.

```sh
gh workflow run ship.yml --ref main -f version=0.1.0 -f dry_run=true
```

Publishing requires `dry_run=false`. That creates tag `vX.Y.Z` and a GitHub release for the workflow's source SHA. An existing tag or release stops the job before it changes either one.

```sh
gh workflow run ship.yml --ref main -f version=0.1.0 -f dry_run=false
```

Assets are `amazon_<version>_darwin_arm64.tar.gz`, `amazon_<version>_darwin_amd64.tar.gz`, `amazon_<version>_linux_arm64.tar.gz`, `amazon_<version>_linux_amd64.tar.gz`, and `checksums.txt`. Each archive contains only the `amazon` binary.

This repository's `GITHUB_TOKEN` cannot update `yashiels/homebrew-tap`. After the release exists, dispatch the tap's formula workflow with a new `request_id`:

```sh
gh workflow run update-formula.yml --repo yashiels/homebrew-tap --ref main -f formula=amazon -f tag=v0.1.0 -f repository=yashiels/amazon-za-cli -f description='Read-only Amazon South Africa shopping CLI' -f artifact_template='amazon_{version}_{target}.tar.gz' -f request_id=UNIQUE_ID
```
