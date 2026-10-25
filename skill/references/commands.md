# amazon commands

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
amazon --version --json
```

Search accepts limits 1..48 (default 10) and pages 1..20. Compare accepts 2..20 distinct ASINs or product URLs. Short links are rejected because redirects are disabled. Watchlists hold at most 100 products.

`info` returns one product; `compare` returns an array. Product fields include `asin`, nullable `title`, `brand`, `price`, `listPrice`, `availability`, `delivery`, `rating`, `category`, `specs`, `images`, `url`, `hydrated` and `warnings`. Money is `{amount,currency}`. Delivery contains `fastest` and `standard` ISO timestamps. Ratings include value, count and percentage histogram keys `1` through `5`.

Search returns `{query,page,items,warnings}`. If hydration fails, ASIN-only items have `hydrated:false` and the top-level warning has `code:"hydrate_failed"`. Compare items can contain `error:{code,status?}`. Inclusion warnings have `{asin,field,status,code}`.

Suggest returns an array of strings. Watch check returns entries with `current`, fixed `baseline`, `lowestSeen`, `drop`, `hitTarget` and timestamps. Target and drop amounts are in ZAR. Unavailable prices leave stored price state unchanged.

Errors are JSON objects `{error,code,status?}` on stdout with nonzero exit status. Doctor refreshes the anonymous session and performs a signed probe, reporting app version, session presence/age and probe result without printing credentials.
