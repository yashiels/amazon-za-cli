---
name: amazon-za
description: Use when an agent must search or price products on amazon.co.za, inspect or compare products, get query suggestions, or manage a local price watchlist through the amazon CLI.
---

Use `amazon` with `--json` for machine-readable results. This CLI is read-only on Amazon: it has no account login, cart, checkout or order operations. Watch commands modify only local files.

Read [references/commands.md](references/commands.md) for command syntax and response shapes.

- Exit 0 means success; 1 means runtime/API failure; 3 is reserved for auth; 4 means invalid arguments.
- If `code` is `blocked_by_waf` or `edge_blocked`, stop. Do not retry, change identities or attempt to bypass the challenge.
- A search result with `hydrated:false` has only an ASIN and product URL. Do not infer a title or price.
- A null price is unavailable. Inclusion warnings identify denied fields; other fields can still be used.
- Batch comparisons preserve the first input order and include per-item errors. Check each item before presenting it as available.
- `watch add` stores a new baseline; re-adding resets it. `watch check` preserves baseline and updates lowest seen prices only when a current ZAR price exists.
- `--max-requests N` lowers a command's request attempt budget. Never increase traffic to work around a block.
