# amazon-za-cli plan (v1)

## Outcome
`amazon` is a read-only Go CLI for amazon.co.za, used by Yashiel and his agents to search, inspect,
compare and price-watch products from the terminal with stable JSON. It has no login, no cart and no
writes to Amazon. Distribution: single static binary, `brew install yashiels/tap/amazon`.

User commands:
- `amazon search "<query>" [--limit N] [--page N] [--json]`
- `amazon info <asin|url> [--json]`
- `amazon compare <asin|url>... [--json]` (2..20 items, one batched API call)
- `amazon suggest "<prefix>" [--json]`
- `amazon watch add|rm|list|check` (local price watchlist, mirrors takealot `watch`)
- `amazon --version`, `amazon --help`

Never: login, cart, wishlist, checkout, order or any POST/PUT/DELETE to Amazon.

## Reverse-engineering evidence (Play APK)
- Source: Google Play via apkeep, package `com.amazon.mShop.android.shopping`, versionName 32.19.0.100,
  versionCode 1243250306, sha256 2e5fa99b75d33a6442614bd028016eb8b3a0c3e510a55a26b445559a46d5952b.
- Amazon API ("AAPI"): host `https://data.amazon.co.za`, marketplace id `AE08WJ6YKNBMC`.
  Path `/api/marketplaces/{mid}/products/{asin}` or `/products/{a1,a2,...}` for a batch.
- Media type in Accept: `application/vnd.com.amazon.api+json; type="<type>"; expand="<inclusions>"`
  (source: `com/amazon/mShop/share/ingress/aapi/AAPIHelper.java` makeAcceptHeaderValue).
  Single = `product/v2`; batch = `collection(product/v2)/v1` (response key `entities[]`).
- Signing (source: `aapi/client/auth/AmazonAPIRequestSigner.java`, `com/amazon/api/authentication/signing/AAPIRequestSigner.java`):
  header `X-AAPI-Signature: <alg> Time=<t> SignedHeaders=<h1;h2..> Signature=<hex>/<keyId>`
  where t = `yyyy-MM-dd'T'HH:mm:ssZ` (UTC, `+0000`), and the HMAC input is
  `METHOD\nTIME\nKEYID\nALG\nPATH\nCANONICAL_QUERY\n` + for each lowercased header name sorted: `name:value\n` + `h1;h2;...`.
  Canonical query = URL-encoded `k=v` pairs sorted and joined with `&` (empty when no query).
  Signed headers are every request header set before signing except `X-Amzn-Trace-Id`; User-Agent is added after signing.
- Keys: anonymous requests are signed with app-embedded HMAC credentials (key id + secret).
  Two were tested: the widget key (HmacSHA1) only returns title/price/images/category; the Fez/AR key
  (`com/a9/fez/aapi/FezAAPICredentials.java`, HmacSHA256) returns title, price, availability, delivery
  promise, by-line, review summary+histogram, product overview/specs, category, images, call-to-action.
  `dealDetails` and `product.offer-comparison-experience/v1` return 403 with both keys.
- Required headers: `Accept`, `Accept-Language: en-ZA`, `X-Amzn-Session-Id`, `X-Amzn-UBID`, signature, and
  User-Agent `Amazon.com/32.19.0.100 (Android/14/Pixel 8)`. Without session headers the API returns 404 empty body;
  unsigned returns 404 empty body. Session/ubid come from cookies `session-id` and `ubid-acbza` set by one GET
  of `https://www.amazon.co.za/` with the app UA.
- Per-inclusion failures are inline: an included field can be `{"type":"error/v1","entity":{"status":403,...}}`
  while the parent is 200. Parsing must treat each inclusion independently.
- Search: no native JSON search route was found in the APK; the app renders `/s` in a webview.
  `GET https://www.amazon.co.za/s?k=<q>&page=<n>` with the app UA returns 200 mobile HTML; a browser UA or curl default
  returns 202 + AWS WAF challenge. Results: `div[data-component-type="s-search-result"][data-asin]` in rank order
  (dedupe; same ASIN can appear twice: sponsored + organic). Search returns only ASIN order; titles/prices are hydrated via the batch AAPI call.
- Suggest: `https://completion.amazon.co.za/api/2017/suggestions?mid=AE08WJ6YKNBMC&alias=aps&prefix=<p>` (no auth, JSON).

Fixtures (real, public catalogue data, no account PII) will be committed under `testdata/`:
single product (Fez key, all inclusions), batch of 4, search HTML page, WAF challenge page, 404 empty, inline 403 inclusion, suggest JSON.

## Architecture (Go 1.23, stdlib only + cobra)
- `internal/aapi`: signer (pure function, golden test against a fixed time/headers vector computed by the Python reference),
  client with exactly one transport function `do(GET path)`; only GET; host pinned to `data.amazon.co.za`; no redirects followed (3xx = error).
- `internal/session`: bootstrap cookies once, cache in `$XDG_CACHE_HOME/amazon-za/session.json` (0600) with 12h TTL; refresh once on 404-empty.
- `internal/web`: search HTML fetch + ASIN extraction (`golang.org/x/net/html` tokenizer), WAF detection (202 or `awsWafCookieDomainList` marker) -> exit 1 code `blocked_by_waf`.
- `internal/product`: typed DTO built from AAPI: asin, title, brand, price{amount,currency}, listPrice?, availability{type,message},
  delivery{fastest,standard: ISO times}, rating{value,count,histogram}, category, specs[{label,value}], images[urls], url.
  Unknown/denied inclusions -> field null plus `warnings[]`, never invented.
- `internal/watch`: JSON file in `$XDG_CONFIG_HOME/amazon-za/watch.json` (0600), `check` re-prices via one batch call and reports drops vs stored price and optional `--target`.
- `cmd/amazon`: cobra commands; ASIN/URL parser accepting `/dp/`, `/gp/product/`, `/gp/aw/d/`, `amzn.to`-style short links are rejected (no redirect follow) with exit 4.

## Agent contract (same as takealot/checkers60)
Exit 0 ok, 1 runtime/API, 3 auth (unused; reserved), 4 usage. `--json` on every command; errors under `--json` print
`{error, code, status?}` on stdout. Human output: aligned text, no tables wider than 100 cols.

## Request budget and politeness
Max 1 request/sec to www, batch AAPI calls of <= 20 ASINs; `compare`/`watch check` use one AAPI call per 20 ASINs.
Retry once on 5xx/timeout with jitter; never retry 4xx.

## Version pinning
App version, UA and key id live in one `internal/aapi/app.go`. `amazon doctor` prints app version pinned, a live
signed probe result, and session age, so drift is diagnosable. Key is read from that file; distribution per F6.

## Improvements over takealot-cli (apply here; backport as separate takealot issues)
1. Single static binary, no Node/Bun runtime; `go test` + golangci-lint v2 in CI under TZ=UTC.
2. `compare` command with one batched call (takealot has none).
3. `doctor` command for version/signing drift (takealot has none; vendor rotation was a named risk).
4. Per-field `warnings[]` instead of silent nulls when an inclusion is denied.
5. Session cache with TTL + single refresh instead of per-call bootstrap.
7. Agent skill in skill-creator format shipped in `skill/` with SKILL.md (rules) + references/commands.md.

## R2 resolutions (Oracle R1 findings 1-7)

F1 signing goldens. Three vector classes, none derived from the Python probe alone:
- (a) Synthetic: test key `test-key-id` / secret bytes `0x00..0x1f`; the test file spells out the full canonical
  string literally (method, time, key id, alg, path, query, each `name:value` line, signed-header list) and the expected
  hex was produced with `printf '%s' "$CANON" | openssl dgst -sha256 -hmac` (independent HMAC implementation).
  Cases: empty query; query `b=2&a=1&a=0&k=a b` -> canonical `a=0&a=1&b=2&k=a+b` (Java URLEncoder: space=`+`, `*` kept, `~` -> `%7E`);
  header names mixed case -> lowercased + sorted; `X-Amzn-Trace-Id` excluded; values used verbatim (no trim);
  duplicate header names are rejected by the client builder (we never send any).
- (b) Accepted-live vector: one request captured from the reference probe that Amazon answered 200: exact time,
  header set, path (batch with literal commas) and resulting signature header, committed as `testdata/signing/accepted.json`.
  The test recomputes it with the embedded production key and must match byte for byte.
- (c) Wire test: an httptest server asserts the transmitted request line path equals the signed path. Live evidence:
  batch path with literal `,` signed literally -> 200; `%2C` -> 404. Go must send literal commas (set `URL.Opaque`/`RawPath`
  explicitly) and the test asserts it.
- Go transport sets only: Accept, Accept-Language, X-Amzn-Session-Id, X-Amzn-UBID, X-AAPI-Signature, User-Agent.
  `DisableCompression: true` so Go does not add `Accept-Encoding` after signing; Host is not part of the signed set
  (matches Java HttpURLConnection request properties); no cookie jar on the AAPI client.

F2 session (probed live with an empty cookie jar on a separate opener):
- headers only, no cookies: 200 with price. cookies only, no headers: 200 but price inclusion has no entity (unusable).
  neither header: same as cookies only. headers present but EMPTY values: 404 empty body (this was the earlier "404").
  fabricated well-formed values `000-0000000-0000000`: 200 with price.
- Therefore: cookies are never sent to data.amazon.co.za. Session = `session-id` and `ubid-acbza` values read from the
  Set-Cookie of one `GET https://www.amazon.co.za/` (no redirects followed; WAF -> fail). Cached 0600 with expiry =
  min(cookie expiry, 7 days). If bootstrap fails, fall back to nothing: exit 1 `session_bootstrap_failed`.
- 404-empty is NOT treated as session expiry. It maps to exit 1 code `aapi_rejected` with hint `run amazon doctor`.
  No automatic refresh loop. `doctor` re-bootstraps the session and runs one signed probe to separate session vs signing drift.

F3 batch/inclusions/warnings/search:
- Batch response `entities[]`; correlate by `entity.asin`, never by index; input ASINs deduped preserving first order;
  ASINs missing from `entities` -> item `{asin, error:{code:"not_found"}}`; an entity whose `type` is `error/v1` ->
  item error with its status/code. Batch never fails as a whole unless the top-level response is non-200.
- Inclusion parser: any node `{type:"error/v1", entity:{status, code}}` (also inside `buyingOptions[]`) sets the field null and
  appends `warnings: [{asin, field:"buyingOptions[0].availability", status:403, code:"403.1"}]`.
- Fixtures to commit (captured live): single full product; batch of 4 incl. one bogus ASIN; product requested with the `dealDetails`
  inclusion, which the Fez key gets inline 403 on (mixed allowed/denied, captured live); empty-headers 404; search HTML; WAF 202 page; suggest JSON.
- Search: parse ranked `s-search-result` ASINs, dedupe keeping first rank, apply `--limit` after dedupe (default 10, max 48),
  `--page` max 20. Then one batch hydrate. If hydrate fails as a whole: still exit 0 with ASIN-only items, each
  `hydrated:false`, plus top-level warning `{code:"hydrate_failed"}`; products without a price keep `price:null` (no error).

F4 transport allowlist (one function `send(req)` all traffic goes through): method must be GET; scheme https; no userinfo,
no port, no fragment; exact (host, path) rules checked on the RAW escaped path before any decoding: `www.amazon.co.za` path exactly `/` (bootstrap) or exactly `/s` (search, query only `k`,`page`); `data.amazon.co.za`
`/api/marketplaces/AE08WJ6YKNBMC/products/`; `completion.amazon.co.za` `/api/2017/suggestions`. Path is checked after
decoding: reject `..`, `%2e`, `\\`, `%2f` in ASIN segment. ASINs validated `^[A-Z0-9]{10}$`. `CheckRedirect` returns
an error for every client. URL inputs accepted only for hosts `www.amazon.co.za`/`amazon.co.za` with `/dp/{asin}`,
`/gp/product/{asin}`, `/gp/aw/d/{asin}`, `/<slug>/dp/{asin}`; anything else exit 4.

F5 limits: connect 5s, TLS 5s, response headers 10s, total per request 20s; body cap 4 MiB (HTML) / 2 MiB (JSON);
at most 1 retry, only on network error or 5xx, counted in budget; immediate stop (no retry) on 403/429/WAF.
Per-command attempt cap = (1 if session bootstrap needed) + operation requests + 1 retry: search = 1 + 2 + 1;
info = 1 + 1 + 1; compare/watch check = 1 + ceil(n/20) + 1; suggest = 0 + 1 + 1; watchlist max 100 items; compare max 20 ASINs. `--max-requests` global flag lowers the cap. Tests assert
exact attempt counts via httptest.

F6 D1 RESOLVED by owner (Yashiel, 2026-10-25): public repo + Homebrew tap; he explicitly accepts the risk of
redistributing the app-embedded HMAC key, since it is already public inside the Play APK. Key lives in one file
`internal/aapi/app.go`; `doctor` never prints key, signature, session or ubid values (presence + age only).

F7 persistence: config/cache dirs created 0700; files written via temp file in the same dir + fsync + rename, mode 0600;
refuse to follow a symlink at the target (Lstat check) and refuse a parent that is a symlink; watch updates under an
flock on `watch.json.lock` (bounded 5s wait, else exit 1 `locked`). Watch semantics: `watch add` fetches the current
price once and stores it as `baseline` with `addedAt`; `--target` optional. `watch check` reports `current`, `baseline`,
`lowestSeen`, `drop` (baseline-current), `hitTarget`; it updates only `lowestSeen` and `lastChecked`, never `baseline`
(`watch add` again resets it). Unavailable price -> reported, nothing updated.

Cuts accepted: widget key and SHA-1 code removed; shared cross-store watch schema dropped; arbitrary-path abstractions dropped.
Added: JSON compatibility policy (fields only added within 0.x minor; removals bump minor and are noted in CHANGELOG);
under `--json` stdout carries only JSON (cobra usage errors routed to the JSON envelope, exit 4); error messages never contain
signature, key id, session, ubid or raw bodies (redaction test with planted values).
Live acceptance replaced: search returns >=5 valid ASINs and >=1 hydrated item with title; info returns title + price for B0H471LF7H.

## Decisions for the reviewer to grade
- D1: RESOLVED by owner: public repo + tap, key-redistribution risk explicitly accepted (see F6).
- D2: Fez key only, widget key unused. Proposal: Fez only.
- D3: Search via mobile HTML with app UA + AAPI hydrate (two hosts) vs scrape full result cards. Proposal: hydrate.

## Tests / acceptance
- Signer golden vectors per F1 (HmacSHA256 only), canonical query sorting, header lowercasing/sort, trace-id exclusion.
- Parsers on every fixture incl. inline 403 inclusions and WAF page.
- Transport tests: non-GET refused, other host refused, 3xx refused.
- Live acceptance (manual, by orchestrator, from the built binary): `search "ssd enclosure" --json` >= 5 valid ASINs and >= 1 hydrated item with title; `info B0H471LF7H --json` has price, rating, availability, delivery; `compare` of 4 ASINs one AAPI call; `suggest`; `watch add/check`; `doctor` green.

## Out of scope v1
Reviews text, offers from other sellers (403), deals (403), login/account, price history (no Serval equivalent known for Amazon ZA).
