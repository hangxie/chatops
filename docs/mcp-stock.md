# stock MCP tool group

Status: designed, not implemented. Selectable with `--tool stock` once built.

The `stock` group answers price questions about listed instruments: stocks, ETFs, mutual funds, and indices. Package: `pkg/mcpserver/stock`.

```text
symbols ───────────────────────┐
names ──▶ search ──▶ resolve ──┴──▶ provider (tiingo, then yahoo) ──▶ cache ──▶ text, or text + PNG
```

| Tool | Answers | Asked for as |
|---|---|---|
| `ticker_lookup` | "what is the ticker for Vanguard S&P 500?" | lookup |
| `ticker_quote` | "how is AAPL doing?" | now |
| `ticker_history` | "VOO's low and high in 2025?" | history |
| `ticker_chart` | "show me NVDA over the last year" | diagram |

All four are read-only: `ReadOnlyHint: true`, `IdempotentHint: true`, `OpenWorldHint: true`. They need no Phase 4 grant beyond read access.

## Data providers

No free source is both official and complete. The group reads from providers behind one interface (`Search`, `Quote`, `Bars`) and fails over between them, as `llm.models` does.

| Provider | Key | Strength | Weakness |
|---|---|---|---|
| `tiingo` | free key | Official API. Stable, documented, 30+ years of daily bars, US stocks, ETFs, and mutual funds | Free tier: 50 requests/hour, 1,000/day, 500 symbols/month, internal use only. No indices. Intraday volume is IEX-only |
| `yahoo` | none | Covers everything, including indices (`^GSPC`) and non-US listings (`7203.T`) | Unofficial and undocumented. Rate-limits without warning (HTTP 429), and its terms forbid redistribution |

Rejected:

| Source | Why |
|---|---|
| Alpha Vantage | 25 requests/day |
| Finnhub | free plan refuses stock candles, so it cannot serve history or charts |
| Twelve Data | 8 requests/minute is one busy turn |
| Polygon (Massive) | 5 requests/minute, delayed, real time only on paid plans |
| Stooq | now behind a JavaScript proof-of-work page |

### Failover

Per request, in priority order:

| Provider answer | Next provider? | Also |
|---|---|---|
| HTTP 429, 5xx, timeout, local rate limit reached | yes | |
| symbol not found | yes | coverage differs: Tiingo has no indices |
| request outside the provider's capability (see [Bars](#bars)) | yes | skipped without a request |
| HTTP 401 or 403 | yes | a warning is logged and the provider is skipped for 10 minutes |
| other 4xx | no | the request itself is wrong |

- The result names the provider that answered, so a Yahoo answer is visible as such.
- Each provider has a local token bucket from its configured limit. Hitting the limit locally fails over at once, rather than spending a request to learn about the 429.
- An auth failure is a configuration problem, not a passing one. So it is logged as `provider tiingo: authentication failed (HTTP 401)` each time it is retried, and never hidden behind a working fallback. The log never includes the key or the URL that carries it.
- One row is never assembled from two providers. A field one provider lacks stays unavailable.

## Prices

Every provider is normalized to the same price basis. Providers can still differ slightly, through corrections, venue coverage, and timestamps. So the basis is the promise, not identical numbers.

- **Split-adjusted, not dividend-adjusted**, to the share basis of the latest session. Without the split adjustment, NVDA's 2024 10:1 split makes every min/max that spans it wrong. With the dividend adjustment, closes no longer match the prices people see quoted.
- Dates and "today" are in the exchange's time zone, not the server's.

| Provider | How |
|---|---|
| `yahoo` | `close`, `open`, `high`, `low`, and `volume` already have this basis, for every interval. Checked on NVDA's 2024-06-10 split: the day before reads 120.89 and 412M shares |
| `tiingo` | raw prices are unadjusted, and `splitFactor` is per day: the ratio of a split taking effect that day, else 1. It is converted as below |

Tiingo conversion:

1. Fetch daily bars from the start date **through the latest session**, whatever the requested end. A split after the requested end still changes the basis, and a shorter fetch would not see it.
2. Walk the bars from newest to oldest with a cumulative factor `f`, starting at 1.
3. For each bar, divide open, high, low, and close by `f` and multiply volume by `f`. Then multiply `f` by that bar's `splitFactor`, because a split day's own bar is already on the new basis.
4. Trim to the requested range.

Tiingo's `resampleFreq` weekly and monthly bars are never used: how they carry `splitFactor` is not documented.

Required test cases for the conversion:

| Case | Expected for bars before the split |
|---|---|
| 10-for-1 split (`splitFactor` 10) | prices divided by 10, volume multiplied by 10; the split day's bar unchanged |
| 1-for-10 reverse split (`splitFactor` 0.1) | prices multiplied by 10, volume divided by 10 |
| requested range ending before a later split | the returned bars are still on the post-split basis |

## Bars

Quotas count requests, not data points: one request returns a whole series. So a chart costs one request, whatever its length. The only volume-based limit is Tiingo's 1 GB/month bandwidth.

| Provider | Fetches | Limit |
|---|---|---|
| `tiingo` | daily bars, start through the latest session | start at most 10 years back (about 600 KB at 230 B/row); older starts go to the next provider |
| `yahoo` | daily bars up to 10 years back, monthly bars beyond | none |

- Daily bars are grouped into weeks or months in Go. So `ticker_history` keeps the exact dates of its low and high, and it shares a cache entry with `ticker_chart`: "VOO's 5-year range, then chart it" is one request.
- The 10-year limit keeps one `max` call from spending several MB of Tiingo's monthly bandwidth. Past it, only Yahoo serves, on monthly bars.
- The bar size is read from the returned timestamps, never assumed from the request. Yahoo's `range=max&interval=1d` silently returns coarser bars (20 KB for AAPL, the same as weekly).
- A bar missing any of open, high, low, or close is dropped, and the result says how many were dropped. A missing volume counts as unknown, not zero.

## Symbols and names

Every price tool takes two lists, and at least one item in total, up to `max_tickers` (default 5):

| Argument | Accepts | Validation |
|---|---|---|
| `symbols` | ticker symbols, used as given | `^[A-Za-z0-9.^=-]{1,20}$` |
| `names` | company or fund names, always searched | trimmed, 1–64 printable characters |

Why two lists: one list cannot tell a name from a symbol. "Coke" as a symbol is `COKE`, Coca-Cola Consolidated, not The Coca-Cola Company (`KO`). The model knows which it was given, so it says so.

- A symbol is never searched. An unknown symbol is a `not found` row that suggests `ticker_lookup`.
- Symbols are free-form, unlike `host`'s addresses. A wrong symbol costs one request, and the cache and token bucket bound that cost.

A name resolves only when the match is unambiguous:

1. Search, and keep stocks, ETFs, mutual funds, and indices. Options, futures, and currencies are dropped. Otherwise "Apple" returns Apple single-stock futures second.
2. Group candidates by issuer, so `AAPL`, `AAPL.TO`, and `APC.DE` are one issuer. An issuer's first-ranked listing stands for it.
3. Compare the name with each issuer's name, ignoring case, punctuation, and trailing words such as `Inc`, `Corp`, `Corporation`, `Ltd`, `plc`, `ETF`, `Fund`, `Shares`, and `Class A`.
4. Exactly one issuer equal to the name resolves. Otherwise, exactly one issuer whose name starts with it resolves.
5. Anything else is an `ambiguous` row listing up to 5 candidates, and the tool fetches nothing for it.

| Name | Resolves to | Why |
|---|---|---|
| `Apple` | `AAPL` | equals "Apple Inc."; Apple Hospitality REIT only starts with it |
| `Microsoft Corporation` | `MSFT` | equals after the suffix is dropped |
| `Vanguard S&P 500` | `VOO` | equals "Vanguard S&P 500 ETF" |
| `Berkshire Hathaway` | `BRK-B` | one issuer; the row adds "also class A: BRK-A" |
| `Ford` | ambiguous | Ford Motor Company and Ford Otomotiv both start with it, none equals it |
| `Vanguard` | ambiguous | many issuers start with it, none equals it |

A resolved row starts with the mapping, `Apple → AAPL (Apple Inc., NASDAQ)`, so the reply shows it.

Required test cases for the resolution, from recorded search responses:

| Case | Expected |
|---|---|
| share classes (`Berkshire Hathaway`: BRK-B, BRK-A) | the first-ranked class, and the row names the other class; never a silent pick |
| cross-listings ranked below the home listing (`Apple`: AAPL, AAPL.TO, APC.DE) | the home listing |
| a foreign listing ranked first | that listing, with its exchange in the mapping |
| ETFs whose names differ by one word (`Vanguard S&P 500`: VOO, VOOG) | VOO only on an equal name; a prefix shared by both is ambiguous |
| debt or preferred listings sharing the issuer's name (`Ford`: F, F-PD) | not grouped with the common stock, since their names differ |

## Tool: `ticker_lookup`

- Argument: `names` (required), a non-empty list. It follows the target-list convention of [`mcp-k8s.md`](mcp-k8s.md#targets).
- Returns up to 5 candidates per name, filtered and grouped as above: symbol, name, type, exchange.

## Tool: `ticker_quote`

- Arguments: `symbols` and `names`, as above.
- One line per ticker. A field the provider does not have is shown as unavailable, never filled from another source or older data.

| Field | `tiingo`, session open | `tiingo`, after the official close | `yahoo` |
|---|---|---|---|
| endpoint | `/iex/<ticker>` | `/iex/<ticker>` | `v8/finance/chart`, today's bar |
| price | `tngoLast`: the last trade or, when the spread is tight, the bid/ask midpoint; labeled "indicative" | `tngoLast`, which switches to the official close | regular-market price |
| previous close, change | yes | yes | yes |
| open, high, low | yes (Tiingo-calculated) | yes | yes |
| volume | unavailable: IEX-only, a small share of the real volume | consolidated | consolidated |
| market state | from the clock, see below | from the clock | from the provider |
| timestamp | yes | yes | yes |

- Tiingo's IEX `last`, bid, and ask need an IEX entitlement and are null on the free tier, so they are not used.
Market state and freshness are separate facts, so a stale feed is never reported as a closed market:

- **Market state** is `open`, `closed`, or `unknown`. Yahoo reports it. Tiingo does not, so it comes from the US clock:

| US clock (America/New_York) | Quote refreshed in the last 15 minutes | State |
|---|---|---|
| weekend, or outside 9:30–16:00 | any | `closed` |
| weekday, 9:30–16:00 | yes | `open` |
| weekday, 9:30–16:00 | no | `unknown`: a holiday, an early close, or a stale feed, which the clock cannot tell apart |

- **Freshness** is the quote's timestamp, always shown, plus `stale` when the state is `open` or `unknown` and the quote is more than 15 minutes old. Free data can be delayed, and the model repeats what it is given.
- After an early close (13:00), a quote refreshed just before it still reads `open` for up to 15 minutes, then `unknown`.
- An exchange holiday calendar would turn some `unknown` into `closed`. It is left out until `unknown` turns out to be common.

## Tool: `ticker_history`

- Arguments: `symbols` and `names`, plus a range.
- Range: `period` is an enum: `1w`, `1m`, `3m`, `6m`, `ytd`, `1y`, `2y`, `5y`, `10y`, `max`. The server computes the dates, because small models do not reliably know today's date.
- Alternatively, `start` and `end` are `YYYY-MM-DD`, for "in 2024" or "since March". `end` defaults to today.
- One line per ticker: low and high with their dates, first open, last close, change, average daily volume.
- Low and high are the bars' intraday extremes, not the lowest and highest closes. Change is from the first bar's open to the last bar's close, so one bar is enough.
- On daily bars, low and high carry their day: `high 237.49 on 2024-12-26`. On monthly bars they carry only the month, and the row says the bars are monthly: `high 237.49 in 2024-12 (monthly bars)`. A first-of-month date would read as the exact day.
- Bars never reach the model. A five-year daily series is about 1,250 rows, and the model needs four numbers.

| Range arguments | Result |
|---|---|
| `period` with `start` or `end` | tool error: give one or the other |
| neither | tool error |
| a date that is not `YYYY-MM-DD` | tool error |
| `start` after `end` | tool error |
| `start` in the future | tool error |
| `end` in the future | clamped to today |
| a range with no trading session (a weekend, before listing) | `no trading sessions` row |
| a range starting before listing | starts at the first bar, and the row says so |

## Tool: `ticker_chart`

- Arguments: as `ticker_history`, but exactly one symbol or name: one chart per image reads better than overlaid series with different price scales.
- Returns a PNG plus the same summary text as `ticker_history`.
- Rendered in-process with `gonum.org/v1/plot` (pure Go, no cgo): closing price as a line, volume as bars in a lower panel sharing the time axis.
- Bars are grouped in Go by period, so a chart stays at most about 260 points:

| Period | Bar |
|---|---|
| up to 1 year | day |
| up to 5 years | week |
| longer | month |

### Images in replies

Today `internal/mcp` replaces image content with `[image/png image omitted]`, and `conversation.Reply` is only `{Text, Footer}`. The chart needs a path from the tool result to the chat post that bypasses the model:

```text
tool result ─┬─ text ─────────────────▶ model ──▶ Reply.Text
             └─ image ─▶ turn's attachments ───▶ Reply.Attachments ──▶ backend upload
```

- The model sees the text plus `[chart attached to the reply]`, never the image bytes. Small models cannot read images, and base64 would fill their context.
- The change is generic: an image from any configured MCP server is attached, not only `stock`'s.
- Only `image/png` and `image/jpeg` are attached, at most 1 MB each. Anything else keeps today's placeholder.

Lifecycle:

- Attachments belong to one turn, and never carry into the next.
- Only calls that completed and succeeded add one. A failed or cancelled call adds none.
- Order is the order the model issued the calls, not the order they finished. So concurrent calls attach deterministically.
- If the model repeats a call with the same server, tool, and arguments, only the last image is kept.
- At most 4 per reply, the first 4 in call order. The reply then says `2 more charts omitted (limit 4)`.
- They go out only with a reply the model answered. A failure reply (model error, iteration limit, timeout) drops them and says `1 chart not sent: the turn failed`.
- A failed upload leaves the text reply in place, with a line `chart upload failed`.

| Backend | Attachment |
|---|---|
| Slack | uploaded into the reply's thread with `files.getUploadURLExternal` and `files.completeUploadExternal`, which need the `files:write` scope. The old `files.upload` is retired |
| Discord | sent as a file attachment on the reply message |
| `stdio`, `unix` | a line `[chart: TICKER 1y, image/png, 48 KB]`; the summary text carries the numbers |

## Caching

An in-process LRU cache, bounded by entry count. Requests are resolved before lookup, so `period: 1y` asked for on two different days is two entries.

| Data | Key | TTL |
|---|---|---|
| search results | provider, name | 24 hours |
| quote | provider, symbol | 60 seconds |
| bars, market closed | provider, symbol, effective start and end, bar size, price basis | 12 hours |
| bars, session open and range reaching today | same | 5 minutes |

- Bars are keyed by symbol and dates, not by tool, so history and chart share them.
- Providers do revise history, after a late split or a correction, even far back. A revision can be up to 12 hours late. A split on a new day starts new entries, because the effective end is part of the key.

## Errors

The same rules as [`mcp-status.md`](mcp-status.md):

| Request | Failure | Result |
|---|---|---|
| one ticker | not found, ambiguous, or unreadable | tool error naming the ticker and the providers tried; ambiguous lists its candidates |
| several | all fail | tool error with the count and the first error |
| several | some fail | a `not found`, `ambiguous`, or `unavailable` row; a tool error would make the model retry and lose the good rows |

- Provider errors never include the API key or the request URL that carries it.

## Scope and licensing

- The sample `config.yaml` prompt's scope gate declines "general knowledge" questions, and a stock price is not an operational request. A deployment with `stock` must widen that prompt to name market data.
- Tiingo's free license is internal use only. Yahoo's terms forbid redistribution. Posting quotes into a channel of your own team is the intended use, and the README says so.

## Configuration

- `--config` is optional with `--tool stock` and names a directory, installed as `/etc/chatops/mcp-stock.d/`.
- Its `00-default.yaml` enables `yahoo` only, so the packaged server starts without a key. `tiingo` is a commented example.
- Providers are a mapping by name, not a list. Config files merge lists by appending, so a list could not put a local `tiingo` ahead of the packaged `yahoo`. A mapping merges by key: a local file adds `tiingo` with a lower `priority`, or sets `yahoo.enabled: false`.
- The API key comes from the environment variable named by `api_key_env`, so it lives in `chatops.env` (0640), never in a 0644 config file.
- Unknown fields, an unknown `type`, and an enabled provider whose key variable is empty stop `chatops-mcp` at startup. A key that is set but wrong shows only on first use, as the auth warning above.

```yaml
providers:
  tiingo:
    type: tiingo
    priority: 10              # lower is tried first
    api_key_env: TIINGO_API_KEY
    requests_per_hour: 50     # local token bucket
  yahoo:
    type: yahoo
    priority: 20
    requests_per_hour: 300
max_tickers: 5
```

## Delivery

| PR | Scope |
|---|---|
| 1 | this design |
| 2 | `ticker_lookup`, `ticker_quote`, `ticker_history`; `tiingo` and `yahoo` providers, cache, packaging |
| 3 | image attachments: `internal/mcp`, `conversation.Reply`, Slack and Discord upload |
| 4 | `ticker_chart` |
