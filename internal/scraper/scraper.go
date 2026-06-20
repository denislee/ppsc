// Package scraper turns a configured Site into a slice of Property records.
//
// Two strategies are supported, both selectable and configurable from the UI:
//
//   - "css":      parse server-rendered HTML with goquery CSS selectors.
//   - "nextdata": extract the embedded __NEXT_DATA__ JSON that Next.js sites
//     (VivaReal, ZAP, OLX, QuintoAndar, ...) ship in the page and
//     map fields via dotted/indexed key paths.
//
// No headless browser is required, which keeps the app light enough to run
// locally. Sites that render results purely client-side from an authenticated
// XHR may need their JSON endpoint used directly as the URLTemplate instead.
package scraper

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"ppsc/internal/models"

	"github.com/PuerkitoBio/goquery"
)

// PageGetter fetches a page's HTML for a URL. Implemented by Fetcher (plain
// HTTP, fast and light) and BrowserFetcher (headless Chrome, for JS-rendered or
// anti-bot-protected sites). Scrape() is agnostic to which one it's handed.
type PageGetter interface {
	Get(ctx context.Context, rawURL string) (string, error)
	SetMinInterval(d time.Duration)
}

// limiter enforces a polite minimum gap (plus jitter) between requests to the
// same host. Shared by both fetchers so politeness is uniform.
type limiter struct {
	mu          sync.Mutex
	lastHit     map[string]time.Time // host -> earliest time the next request may fire
	minInterval time.Duration
}

func newLimiter(d time.Duration) *limiter {
	return &limiter{lastHit: make(map[string]time.Time), minInterval: d}
}

// SetMinInterval sets the minimum delay between requests to the same host.
// Values below 1s are clamped up to 1s to stay polite.
func (l *limiter) SetMinInterval(d time.Duration) {
	if d < time.Second {
		d = time.Second
	}
	l.mu.Lock()
	l.minInterval = d
	l.mu.Unlock()
}

// reserve claims the next available slot for host and returns how long the
// caller must wait before firing, recording the reservation so concurrent and
// successive calls for the same host queue up instead of bursting.
func (l *limiter) reserve(host string) (wait, jitter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	earliest := l.lastHit[host]
	if earliest.Before(now) {
		earliest = now
	}
	wait = earliest.Sub(now)
	if l.minInterval > 0 {
		jitter = rand.N(l.minInterval / 2)
	}
	l.lastHit[host] = earliest.Add(l.minInterval + jitter)
	return wait, jitter
}

// wait blocks until this host's reserved slot, honouring ctx cancellation.
func (l *limiter) wait(ctx context.Context, host string) error {
	w, j := l.reserve(host)
	if d := w + j; d > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
		}
	}
	return nil
}

func hostOf(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		return u.Host
	}
	return rawURL
}

// Fetcher retrieves a URL's body over plain HTTP while being deliberately
// polite: per-host throttle + jitter, and 429/503 backoff honouring
// Retry-After. A 401/403 is treated as terminal (the site is blocking us) —
// those sites should use a BrowserFetcher instead.
type Fetcher struct {
	client     *http.Client
	maxRetries int
	lim        *limiter
}

func NewFetcher() *Fetcher {
	return &Fetcher{
		client:     &http.Client{Timeout: 30 * time.Second},
		maxRetries: 3,
		lim:        newLimiter(5 * time.Second),
	}
}

func (f *Fetcher) SetMinInterval(d time.Duration) { f.lim.SetMinInterval(d) }

func (f *Fetcher) Get(ctx context.Context, rawURL string) (string, error) {
	host := hostOf(rawURL)

	for attempt := 0; ; attempt++ {
		if err := f.lim.wait(ctx, host); err != nil {
			return "", err
		}

		body, status, retryAfter, err := f.do(ctx, rawURL)
		if err != nil {
			return "", err
		}
		switch {
		case status < 400:
			return body, nil
		case (status == 429 || status == 503) && attempt < f.maxRetries:
			back := backoff(attempt, retryAfter)
			slog.Warn("rate-limited; backing off", "host", host, "status", status, "wait", back, "attempt", attempt+1)
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(back):
			}
			continue
		case status == 403 || status == 401:
			return "", fmt.Errorf("http %d — site is blocking automated requests (likely needs a real browser)", status)
		default:
			return "", fmt.Errorf("http %d (%s)", status, http.StatusText(status))
		}
	}
}

// do performs a single request and returns the body, status, and parsed
// Retry-After delay (if any).
func (f *Fetcher) do(ctx context.Context, rawURL string) (body string, status int, retryAfter time.Duration, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", 0, 0, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "pt-BR,pt;q=0.9,en;q=0.8")
	resp, err := f.client.Do(req)
	if err != nil {
		return "", 0, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", resp.StatusCode, 0, err
	}
	return string(b), resp.StatusCode, parseRetryAfter(resp.Header.Get("Retry-After")), nil
}

// backoff returns an exponential delay (capped), preferring the server's
// Retry-After hint when present.
func backoff(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return retryAfter
	}
	d := min(time.Duration(1<<attempt)*2*time.Second, 60*time.Second) // 2s, 4s, 8s, … cap 60s
	return d + rand.N(time.Second)
}

func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// BuildURL substitutes the supported placeholders in a site's URL template.
func BuildURL(tmpl string, f models.Filters, page int) string {
	rep := strings.NewReplacer(
		"{query}", urlValue(f.Query),
		"{minPrice}", i64s(f.MinPrice),
		"{maxPrice}", i64s(f.MaxPrice),
		"{minBeds}", strconv.Itoa(f.MinBedrooms),
		"{minArea}", strconv.Itoa(f.MinAreaM2),
		"{neighborhood}", urlValue(f.Neighborhood),
		"{page}", strconv.Itoa(page),
	)
	return rep.Replace(tmpl)
}

func urlValue(s string) string { return strings.ReplaceAll(strings.TrimSpace(s), " ", "+") }
func i64s(v int64) string {
	if v == 0 {
		return ""
	}
	return strconv.FormatInt(v, 10)
}

// Scrape runs a single site through its strategy for the given filters,
// returning the discovered (un-deduplicated, un-post-filtered) properties.
func Scrape(ctx context.Context, getter PageGetter, site models.Site, filters models.Filters) ([]models.Property, error) {
	var all []models.Property
	pages := max(site.MaxPages, 1)
	for page := 1; page <= pages; page++ {
		url := BuildURL(site.URLTemplate, filters, page)
		body, err := getter.Get(ctx, url)
		if err != nil {
			return all, err
		}
		var props []models.Property
		switch site.Strategy {
		case models.StrategyNextData:
			props, err = parseNextData(body, site)
		default:
			props, err = parseCSS(body, site)
		}
		slog.Debug("fetched page", "site", site.Name, "page", page, "url", url,
			"bytes", len(body), "parsed", len(props), "err", err)
		if err != nil {
			return all, err
		}
		all = append(all, props...)
		if len(props) == 0 {
			break // no more results; stop paginating
		}
	}
	for i := range all {
		all[i].SiteID = site.ID
		all[i].SiteName = site.Name
		all[i].Fingerprint = fingerprint(all[i])
	}
	return all, nil
}

func fingerprint(p models.Property) string {
	key := strings.ToLower(strings.TrimSpace(p.URL))
	if key == "" {
		key = strings.ToLower(p.Title) + "|" + strconv.FormatInt(p.Price, 10) + "|" + strings.ToLower(p.Address)
	}
	sum := sha1.Sum([]byte(key))
	return fmt.Sprintf("%x", sum)
}

// ---- CSS strategy ----

func parseCSS(body string, site models.Site) ([]models.Property, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	sel := site.Selectors
	if sel.Item == "" {
		return nil, fmt.Errorf("css strategy requires an 'item' selector")
	}
	attrURL := orDefault(sel.AttrURL, "href")
	attrImg := orDefault(sel.AttrImage, "src")

	var out []models.Property
	doc.Find(sel.Item).Each(func(_ int, s *goquery.Selection) {
		p := models.Property{}
		p.Title = cssText(s, sel.Title)
		p.URL = absURL(sel.URLPrefix, cssAttr(s, sel.URL, attrURL))
		p.ImageURL = cssAttr(s, sel.Image, attrImg)
		p.Price = parseBRL(cssText(s, sel.Price))
		p.Address = cssText(s, sel.Address)
		p.Neighborhood = CleanNeighborhood(cssText(s, sel.Neighborhood))
		p.Bedrooms = parseInt(cssText(s, sel.Bedrooms))
		p.Bathrooms = parseInt(cssText(s, sel.Bathrooms))
		p.ParkingSpots = parseInt(cssText(s, sel.ParkingSpots))
		p.AreaM2 = parseInt(cssText(s, sel.AreaM2))
		p.Description = cssText(s, sel.Description)
		p.Latitude = parseFloat(cssText(s, sel.Latitude))
		p.Longitude = parseFloat(cssText(s, sel.Longitude))
		if p.Title != "" || p.URL != "" {
			out = append(out, p)
		}
	})
	return out, nil
}

// ExtractDetailPhotos pulls gallery image URLs from a listing's detail-page
// HTML. If sel.DetailPhotos is set it is used as a CSS selector over <img>/
// <source> elements; otherwise photos are discovered generically from
// og:image meta tags and JSON-LD "image" fields. URLs are normalised with
// sel.PhotoPrefix, de-duplicated in order, and obvious non-photos dropped.
func ExtractDetailPhotos(html string, sel models.Selectors) []string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil
	}
	var urls []string
	add := func(u string) { urls = append(urls, absURL(sel.PhotoPrefix, strings.TrimSpace(u))) }

	if sel.DetailPhotos != "" {
		attr := sel.DetailPhotoAttr
		doc.Find(sel.DetailPhotos).Each(func(_ int, s *goquery.Selection) {
			if attr != "" {
				if v, ok := s.Attr(attr); ok {
					add(v)
					return
				}
			}
			// Try the usual image-bearing attributes in order.
			for _, a := range []string{"src", "data-src", "data-lazy", "content"} {
				if v, ok := s.Attr(a); ok && v != "" {
					add(v)
					return
				}
			}
			if v, ok := s.Attr("srcset"); ok {
				add(largestFromSrcset(v))
			}
		})
	} else {
		// Generic discovery: og:image / twitter:image meta tags …
		doc.Find(`meta[property="og:image"], meta[property="og:image:url"], meta[property="og:image:secure_url"], meta[name="twitter:image"]`).
			Each(func(_ int, s *goquery.Selection) {
				if v, ok := s.Attr("content"); ok {
					add(v)
				}
			})
		// … and any JSON-LD "image" fields (string, array, or {url}).
		doc.Find(`script[type="application/ld+json"]`).Each(func(_ int, s *goquery.Selection) {
			for _, u := range jsonLDImages(s.Text()) {
				add(u)
			}
		})
	}
	return dedupePhotos(urls)
}

// largestFromSrcset returns the last (typically highest-resolution) URL in a
// srcset attribute value.
func largestFromSrcset(srcset string) string {
	parts := strings.Split(srcset, ",")
	if len(parts) == 0 {
		return ""
	}
	last := strings.TrimSpace(parts[len(parts)-1])
	return strings.TrimSpace(strings.SplitN(last, " ", 2)[0])
}

// jsonLDImages extracts image URLs from a JSON-LD blob's "image" field, which
// may be a string, an array of strings, or an array of {url} objects.
func jsonLDImages(blob string) []string {
	var root any
	if err := json.Unmarshal([]byte(strings.TrimSpace(blob)), &root); err != nil {
		return nil
	}
	var out []string
	var visit func(v any)
	collect := func(img any) {
		switch x := img.(type) {
		case string:
			out = append(out, x)
		case []any:
			for _, e := range x {
				switch ev := e.(type) {
				case string:
					out = append(out, ev)
				case map[string]any:
					if u, ok := ev["url"].(string); ok {
						out = append(out, u)
					}
				}
			}
		case map[string]any:
			if u, ok := x["url"].(string); ok {
				out = append(out, u)
			}
		}
	}
	visit = func(v any) {
		switch n := v.(type) {
		case map[string]any:
			if img, ok := n["image"]; ok {
				collect(img)
			}
			for _, sub := range n {
				visit(sub)
			}
		case []any:
			for _, sub := range n {
				visit(sub)
			}
		}
	}
	visit(root)
	return out
}

// dedupePhotos removes duplicates (in order) and drops values that clearly are
// not listing photos (non-http, data URIs, svg, sprites/logos/icons).
func dedupePhotos(urls []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, u := range urls {
		u = strings.TrimSpace(u)
		if u == "" || seen[u] {
			continue
		}
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			continue
		}
		low := strings.ToLower(u)
		if strings.HasSuffix(low, ".svg") {
			continue
		}
		skip := false
		for _, junk := range []string{"sprite", "/logo", "logo.", "/icon", "icon.", "favicon", "placeholder", "blank."} {
			if strings.Contains(low, junk) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		seen[u] = true
		out = append(out, u)
	}
	return out
}

// cssText returns the trimmed text of the first match of selector within s.
// An empty selector means "use s itself".
func cssText(s *goquery.Selection, selector string) string {
	if selector == "" {
		return strings.TrimSpace(s.Text())
	}
	return strings.TrimSpace(s.Find(selector).First().Text())
}

func cssAttr(s *goquery.Selection, selector, attr string) string {
	target := s
	if selector != "" {
		target = s.Find(selector).First()
	}
	v, _ := target.Attr(attr)
	return strings.TrimSpace(v)
}

// ---- NextData (embedded JSON) strategy ----

var nextDataRe = regexp.MustCompile(`(?s)<script[^>]*id="__NEXT_DATA__"[^>]*>(.*?)</script>`)

func parseNextData(body string, site models.Site) ([]models.Property, error) {
	m := nextDataRe.FindStringSubmatch(body)
	if m == nil {
		return nil, fmt.Errorf("no __NEXT_DATA__ found; site may not be a Next.js app or is blocking the request")
	}
	var root any
	if err := json.Unmarshal([]byte(m[1]), &root); err != nil {
		return nil, fmt.Errorf("decode __NEXT_DATA__: %w", err)
	}
	sel := site.Selectors
	var items []any
	switch node := jsonPath(root, sel.Item).(type) {
	case []any:
		items = node
	case map[string]any:
		// Some sites (e.g. QuintoAndar) key listings by ID rather than using an
		// array; iterate the map's values in a stable, key-sorted order.
		keys := make([]string, 0, len(node))
		for k := range node {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			items = append(items, node[k])
		}
	default:
		return nil, fmt.Errorf("item path %q did not resolve to a JSON array or object", sel.Item)
	}
	var out []models.Property
	for _, it := range items {
		p := models.Property{
			Title:        jsonStr(it, sel.Title),
			URL:          absURL(sel.URLPrefix, jsonStr(it, sel.URL)),
			ImageURL:     jsonStr(it, sel.Image),
			Price:        parseBRL(jsonStr(it, sel.Price)),
			Address:      jsonStr(it, sel.Address),
			Neighborhood: CleanNeighborhood(jsonStr(it, sel.Neighborhood)),
			Bedrooms:     parseInt(jsonStr(it, sel.Bedrooms)),
			Bathrooms:    parseInt(jsonStr(it, sel.Bathrooms)),
			ParkingSpots: parseInt(jsonStr(it, sel.ParkingSpots)),
			AreaM2:       parseInt(jsonStr(it, sel.AreaM2)),
			Description:  jsonStr(it, sel.Description),
			Latitude:     parseFloat(jsonStr(it, sel.Latitude)),
			Longitude:    parseFloat(jsonStr(it, sel.Longitude)),
		}
		if p.Title != "" || p.URL != "" {
			out = append(out, p)
		}
	}
	return out, nil
}

// jsonPath walks a decoded JSON value using a dotted path. Numeric segments
// index into arrays, e.g. "props.pageProps.results.0.address.city".
func jsonPath(v any, path string) any {
	if path == "" {
		return v
	}
	cur := v
	for seg := range strings.SplitSeq(path, ".") {
		switch node := cur.(type) {
		case map[string]any:
			cur = node[seg]
		case []any:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx < 0 || idx >= len(node) {
				return nil
			}
			cur = node[idx]
		default:
			return nil
		}
		if cur == nil {
			return nil
		}
	}
	return cur
}

// jsonStr resolves a path and renders the leaf as a string.
func jsonStr(v any, path string) string {
	if path == "" {
		return ""
	}
	switch x := jsonPath(v, path).(type) {
	case string:
		return strings.TrimSpace(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	case nil:
		return ""
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

// ---- shared helpers ----

// listingProseWords are tokens that appear in a listing's descriptive heading
// ("Sobrado para comprar com 130 m², 2 quartos … 3 vagas em …") but never in a
// bare neighborhood name. Their presence means a selector captured prose where a
// neighborhood was expected — as ZAP and VivaReal cards do, putting the heading
// in the element our neighborhood selector targets.
var listingProseWords = []string{
	"para comprar", "para alugar", "para vender",
	"quarto", "banheiro", "vaga", "suíte", "suite",
	"dormitório", "dormitorio", "m²", " m2",
}

// looksLikeListingProse reports whether s is a listing-description blob rather
// than a neighborhood name.
func looksLikeListingProse(s string) bool {
	low := strings.ToLower(s)
	for _, w := range listingProseWords {
		if strings.Contains(low, w) {
			return true
		}
	}
	return false
}

// emSplitRe splits on the Portuguese " em " ("in") that precedes the location in
// a listing heading, e.g. "… 3 vagas em Vila Medeiros, São Paulo".
var emSplitRe = regexp.MustCompile(`(?i)\s+em\s+`)

// citySuffixRe matches a trailing " - <City> - <UF>" that some sources append to
// the bairro, e.g. "Mooca - São Paulo - SP". Stripping it collapses these onto
// the bare bairro so the filter dropdown isn't split between "Mooca" and
// "Mooca - São Paulo - SP". The UF is restricted to the 27 Brazilian state codes
// so legitimate hyphenated names (e.g. "Sítio do Mandaqui - Zona Norte") survive.
var citySuffixRe = regexp.MustCompile(`(?i)\s+-\s+[^-]+\s+-\s+(?:AC|AL|AP|AM|BA|CE|DF|ES|GO|MA|MT|MS|MG|PA|PB|PR|PE|PI|RJ|RN|RS|RO|RR|SC|SP|SE|TO)\s*$`)

// stripCitySuffix removes a trailing ", <City>" or " - <City> - <UF>" locality
// tail from a bairro, leaving just the neighborhood name.
func stripCitySuffix(n string) string {
	return strings.TrimSpace(citySuffixRe.ReplaceAllString(n, ""))
}

// CleanNeighborhood normalises a scraped neighborhood. A clean value passes
// through (whitespace-collapsed, locality suffix stripped) unchanged otherwise.
// A descriptive heading captured by a mis-targeted selector is reduced to the
// bairro mined from its trailing "… em <Bairro>, <City>", or "" when no bairro
// can be recovered — an empty neighborhood is better than prose polluting the
// filter dropdown and geocoding queries (the latter being why such listings
// failed to locate on the map).
func CleanNeighborhood(raw string) string {
	n := stripCitySuffix(strings.Join(strings.Fields(raw), " "))
	if n == "" || !looksLikeListingProse(n) {
		return n
	}
	segs := emSplitRe.Split(n, -1)
	tail := strings.TrimSpace(segs[len(segs)-1])
	if i := strings.LastIndex(tail, ","); i >= 0 {
		tail = strings.TrimSpace(tail[:i]) // drop the trailing ", <City>"
	}
	tail = stripCitySuffix(tail)
	if tail != "" && !looksLikeListingProse(tail) {
		return tail
	}
	return ""
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func absURL(prefix, u string) string {
	u = strings.TrimSpace(u)
	if u == "" {
		return ""
	}
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		return u
	}
	if prefix == "" {
		return u
	}
	return strings.TrimRight(prefix, "/") + "/" + strings.TrimLeft(u, "/")
}

var digitsRe = regexp.MustCompile(`\d+`)

// parseBRL extracts a whole-real amount from strings like "R$ 1.250.000",
// "1250000.00", or "R$ 1,2 milhões" (best effort).
func parseBRL(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	low := strings.ToLower(s)
	// Strip currency and thousands separators; treat ',' as decimal.
	clean := strings.NewReplacer("r$", "", " ", "", ".", "").Replace(low)
	clean = strings.SplitN(clean, ",", 2)[0] // drop cents
	digits := digitsRe.FindString(clean)
	if digits == "" {
		return 0
	}
	v, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0
	}
	if strings.Contains(low, "milh") { // "milhão"/"milhões"
		v *= 1_000_000
	}
	return v
}

// parseInt extracts the first integer found in s.
func parseInt(s string) int {
	d := digitsRe.FindString(s)
	if d == "" {
		return 0
	}
	v, _ := strconv.Atoi(d)
	return v
}

// parseFloat parses a decimal coordinate, tolerating a comma decimal separator.
func parseFloat(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	s = strings.ReplaceAll(s, ",", ".")
	v, _ := strconv.ParseFloat(s, 64)
	return v
}
