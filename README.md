# ppsc — property purchase search engine

A small, self-hosted Go app that periodically scrapes configurable real-estate
sites (defaults target **São Paulo** sale listings) and presents the results in
a web UI where you browse listings and manage the sites, filters, and schedule.
Everything runs locally and is stored in a single SQLite file.

## Run

```bash
go build -o ppsc .
./ppsc                       # http://127.0.0.1:8080, db ./ppsc.db, log ./ppsc.log
./ppsc -addr 127.0.0.1:9000 -db /path/to/ppsc.db
./ppsc -log /var/log/ppsc.log -debug   # verbose, custom log path
```

Flags: `-addr`, `-db`, `-log` (empty = console only), `-photos` (photo dir), `-debug`.

`make run` binds to `0.0.0.0:8080` so other devices on your LAN can reach the
UI — it prints the shareable `http://<lan-ip>:8080/` address on startup while
opening the local browser on loopback. Bind to loopback only with
`make run ADDR=127.0.0.1:8080`. (The bare `./ppsc` binary still defaults to
loopback-only `127.0.0.1:8080`; pass `-addr 0.0.0.0:8080` to expose it.)

Open the URL, set your **Filters & Schedule**, enable the sites you want, then
hit **Scrape now** (or let the scheduler run on its interval).

## How it works

- **Storage** — pure-Go SQLite (`modernc.org/sqlite`, no CGO). Sites, settings,
  and de-duplicated listings live in `ppsc.db`.
- **Scheduler** — background loop scrapes every *N* minutes (0 = manual only).
  New listings are kept; re-seen ones just refresh `last_seen`/price. Progress is
  tracked per site, so if the process is killed mid-pass the next launch detects
  the interrupted run and the top bar offers **Resume** (continue the sites that
  weren't done) or **Start over** (a fresh full pass).
- **Scrapers** — two strategies, both fully configurable from the UI so you can
  add **any** site without writing code:
  - **`css`** — parse server-rendered HTML with CSS selectors (`goquery`).
    `item` selects the repeating card; other fields are selectors relative to it.
  - **`nextdata`** — extract the embedded `__NEXT_DATA__` JSON that Next.js
    portals (OLX, QuintoAndar, Lello…) ship in the page. `item` is a
    dotted/indexed path to the listings array (e.g.
    `props.pageProps.results.listings`); fields are paths relative to each item.
    `item` may resolve to an array **or** an object/map (its values are
    iterated, key-sorted — that's how QuintoAndar's ID-keyed listings work).

  Independently of the parse strategy, each site has a **fetch mode**:

  - **HTTP (default)** — fast, light, polite plain HTTP.
  - **Headless browser** — toggle *"Render with headless browser"* on a site to
    fetch it through real Chrome/Chromium (via `chromedp`). This runs the page's
    JavaScript and clears many anti-bot challenges, so JS-rendered or protected
    sites (VivaReal, ZAP) work. It's slower and needs a Chrome/Chromium binary
    on the machine (auto-detected at startup; install `chromium` if missing).

  URL templates support placeholders filled from your global filters:
  `{query} {minPrice} {maxPrice} {minBeds} {minArea} {neighborhood} {page}`.

Use the **Test** button on a site to fetch the first page live and preview what
the current selectors parse — iterate until the sample looks right, then Save.

## Browsing: filters, favorites & photos

- **Filters** on the Listings view (separate from the global scrape filters):
  search text, neighborhood, min/max price, min bedrooms, min area, status, and
  sort — applied instantly to what's already been scraped. A **Clear** button
  resets them.
- **Favorites** — click **♡ Like** on any card to tag listings you like (an
  independent flag, separate from new/seen/hidden triage). The **♥ Favorites**
  toggle in the toolbar filters to just those, and the header shows the count.
- **Photos** — when *Download listing photos* is on (Filters & Schedule → Photos),
  each **new** listing's detail page is visited once and its gallery is
  downloaded to `./photos/<id>/` (served at `/photos`). A 📷 badge on the card
  shows the count; click the thumbnail for a full-screen gallery. Photo fetching
  is gated to new listings and **capped per run** (logged) so a fresh database
  doesn't fetch everything at once — the rest are picked up on later scrapes.
  Galleries are discovered from each site's `og:image` + JSON-LD images by
  default; set a per-site **detail-page gallery selector** (and *render detail
  pages with browser* for JS/anti-bot sites) for precise control. Needs the same
  Chrome/Chromium as the browser fetch mode when a site's detail pages are
  JS-rendered.

## Logs & troubleshooting

Structured logs (`slog`, key=value text) are written to **both the console and a
log file** (`ppsc.log` by default; change with `-log`, or set `-log ""` for
console only). Every scrape run records, per site, how many listings were seen
vs. newly added, how long it took, and the exact error on failure:

```
level=INFO  msg="site scraped"      site="VivaReal — SP (venda)" seen=34 new=12 took=910ms
level=ERROR msg="site scrape failed" site="OLX …" strategy=nextdata err="http 403 …"
level=INFO  msg="scrape run complete" sites=3 new=18 took=4.2s
```

Run with `-debug` to also log every page fetched — its URL, bytes downloaded,
and how many items parsed — which is the fastest way to tell whether a site is
blocking you (small/zero bytes or an `http 4xx`) versus your selectors being
wrong (bytes downloaded but `parsed=0`). Each site's most recent status/error is
also stored in the DB and shown in the Sites list. Tail the file live with
`tail -f ppsc.log`; rotate it with your OS's `logrotate` if it grows large.

## Being polite / avoiding blocks

Real-estate portals rate-limit and actively defend against scrapers, so the
fetcher is deliberately gentle:

- **Per-host throttle** — requests to the same site are serialised with a
  configurable minimum gap (default **5s**, floor 1s), set under
  **Filters & Schedule → Politeness**.
- **Jitter** — a random delay (up to half the interval) is added so the cadence
  doesn't look like a metronome.
- **Backoff** — on HTTP 429/503 it waits and retries (honouring `Retry-After`),
  up to 3 times.
- **No hammering walls** — a 401/403 is treated as terminal (the site is
  blocking automated access), so it fails fast instead of pounding.

The default scrape interval is 6 hours and pagination defaults to 1–2 pages —
keep these conservative. Respect each site's Terms of Service and `robots.txt`.

## Seeded sites & their status

The DB is seeded with starting configs, plus a blank CSS template. **These are
starting points, not guarantees** — portals reshape their JSON often. Status as
last verified:

| Site | Strategy | Fetch | Status |
|------|----------|-------|--------|
| **OLX** | nextdata | HTTP | ✅ ~50/page; rate-limits if hit hard — *enabled* |
| **QuintoAndar** | nextdata | HTTP | ✅ ~14/page embedded (rest via XHR) — *enabled* |
| **Lello** | nextdata | HTTP | ✅ 20/page — *enabled* |
| **VivaReal** | css | 🌐 browser | ✅ ~30/page via headless Chrome (`data-cy` selectors) — *enabled* |
| **ZAP** | css | 🌐 browser | ✅ ~30/page, same markup as VivaReal — *enabled* |
| **Pacheco** | css | 🌐 browser | ✅ ~16/page (title/area/beds/URL). Site publishes **no price** on results — *enabled* |
| **Sinai** | css | 🌐 browser | ✅ ~12/page. Mixes sale+rent+commercial — set a **Min price** to drop rentals — *enabled* |

VivaReal/ZAP sit behind a Cloudflare-style anti-bot wall that 403s a plain HTTP
client; Pacheco and Sinai render their listing details (area, beds, prices)
client-side. All four therefore use the **headless browser** fetch mode (already
set on their seed configs). Two site-specific caveats baked into their notes:
**Pacheco** doesn't show prices on its results page (filter by neighbourhood /
area instead), and **Sinai**'s `/imoveis` mixes sale, rent and commercial — set
a Min price filter to exclude the monthly rentals. When a site changes or
blocks, fix the `item`/field paths from a live page using the **Test** button
(it respects the fetch mode), or add a new site entirely from the UI.

## Layout

```
main.go                  wiring, embedded web UI, graceful shutdown
internal/models          shared types (Property, Site, Filters, Settings)
internal/store           SQLite store (+ migrations) + default site seeds
internal/scraper         HTTP + headless-browser fetchers, throttle/backoff,
                         URL templating, css + nextdata extraction
internal/scheduler       periodic + on-demand scrape runner, post-filtering,
                         photo-download pipeline (new listings, capped per run)
internal/photos          downloads listing galleries to local disk
internal/server          JSON API + static UI handlers + /photos file server
internal/logging         slog setup (console + file)
web/                     single-page UI (index.html, style.css, app.js)
```

## Test

```bash
go test ./...
```
