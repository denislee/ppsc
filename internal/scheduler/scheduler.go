// Package scheduler runs scrape passes across all enabled sites, both on a
// periodic timer and on demand, and persists the results.
package scheduler

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"ppsc/internal/geocode"
	"ppsc/internal/metro"
	"ppsc/internal/models"
	"ppsc/internal/photos"
	"ppsc/internal/scraper"
	"ppsc/internal/store"
)

type Scheduler struct {
	store   *store.Store
	fetcher *scraper.Fetcher
	browser *scraper.BrowserFetcher
	photos  *photos.Manager

	mu       sync.Mutex
	running  bool
	lastRun  time.Time
	lastMsg  string
	resetCh  chan struct{} // wakes the loop to pick up a new interval
	cancelMu sync.Mutex
}

func New(s *store.Store, f *scraper.Fetcher, b *scraper.BrowserFetcher, ph *photos.Manager) *Scheduler {
	return &Scheduler{store: s, fetcher: f, browser: b, photos: ph, resetCh: make(chan struct{}, 1)}
}

// Status reports current scheduler state for the UI.
type Status struct {
	Running bool      `json:"running"`
	LastRun time.Time `json:"last_run"`
	Message string    `json:"message"`
}

func (sc *Scheduler) Status() Status {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return Status{Running: sc.running, LastRun: sc.lastRun, Message: sc.lastMsg}
}

// Kick asks the periodic loop to re-read the interval (call after settings change).
func (sc *Scheduler) Kick() {
	select {
	case sc.resetCh <- struct{}{}:
	default:
	}
}

// Loop runs until ctx is cancelled, scraping every IntervalMinutes.
func (sc *Scheduler) Loop(ctx context.Context) {
	for {
		set, _ := sc.store.GetSettings(ctx)
		interval := time.Duration(set.IntervalMinutes) * time.Minute
		if set.IntervalMinutes <= 0 {
			// Scheduler disabled: wait for a settings change or shutdown.
			select {
			case <-ctx.Done():
				return
			case <-sc.resetCh:
				continue
			}
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-sc.resetCh:
			timer.Stop()
			continue
		case <-timer.C:
			if _, err := sc.RunAll(ctx); err != nil {
				slog.Error("scheduled scrape failed", "err", err)
			}
		}
	}
}

// RunAll scrapes every enabled site once. Returns the number of newly added
// properties. Safe to call concurrently — overlapping calls are skipped.
func (sc *Scheduler) RunAll(ctx context.Context) (int, error) {
	sc.mu.Lock()
	if sc.running {
		sc.mu.Unlock()
		return 0, nil
	}
	sc.running = true
	sc.mu.Unlock()
	defer func() {
		sc.mu.Lock()
		sc.running = false
		sc.lastRun = time.Now()
		sc.mu.Unlock()
	}()

	set, err := sc.store.GetSettings(ctx)
	if err != nil {
		return 0, err
	}
	// Apply the configured politeness delay before each pass so changes from
	// the UI take effect on the next run.
	delay := time.Duration(set.RequestDelaySeconds) * time.Second
	sc.fetcher.SetMinInterval(delay)
	sc.browser.SetMinInterval(delay)

	sites, err := sc.store.ListSites(ctx)
	if err != nil {
		return 0, err
	}

	runStart := time.Now()
	var totalNew int
	var ranSites int
	for _, site := range sites {
		if !site.Enabled {
			continue
		}
		ranSites++
		started := time.Now()
		newCount, seen, err := sc.runSite(ctx, site, set.Filters)
		elapsed := time.Since(started).Round(time.Millisecond)
		status, errMsg := "ok", ""
		if err != nil {
			status, errMsg = "error", err.Error()
			slog.Error("site scrape failed",
				"site", site.Name, "strategy", site.Strategy, "new", newCount, "took", elapsed, "err", err)
		} else {
			slog.Info("site scraped",
				"site", site.Name, "seen", seen, "new", newCount, "took", elapsed)
		}
		_ = sc.store.UpdateSiteRun(ctx, site.ID, status, errMsg, newCount, time.Now())
		totalNew += newCount
	}

	slog.Info("scrape run complete",
		"sites", ranSites, "new", totalNew, "took", time.Since(runStart).Round(time.Millisecond))

	if set.DownloadPhotos {
		sc.fetchPhotos(ctx, set, sites)
	}

	sc.resolveMetro(ctx, set)

	sc.mu.Lock()
	sc.lastMsg = formatMsg(ranSites, totalNew)
	sc.mu.Unlock()
	return totalNew, nil
}

// fetchPhotos visits the detail page of each listing that still needs photos
// (capped per run), extracts the gallery, and downloads it to disk. Detail
// pages are fetched politely through the same throttled getters.
func (sc *Scheduler) fetchPhotos(ctx context.Context, set models.Settings, sites []models.Site) {
	if sc.photos == nil {
		return
	}
	byID := make(map[int64]models.Site, len(sites))
	for _, s := range sites {
		byID[s.ID] = s
	}
	targets, err := sc.store.PropertiesNeedingPhotos(ctx, set.MaxPhotoFetchesPerRun)
	if err != nil {
		slog.Error("photos: query targets", "err", err)
		return
	}
	if len(targets) == 0 {
		return
	}
	pending, _ := sc.store.PropertiesNeedingPhotos(ctx, set.MaxPhotoFetchesPerRun+1)
	if len(pending) > len(targets) {
		slog.Info("photos: capping this run", "limit", set.MaxPhotoFetchesPerRun, "note", "remaining listings get photos on later runs")
	}

	start := time.Now()
	var ok, total int
	for _, t := range targets {
		site := byID[t.SiteID]
		getter := sc.detailGetter(site)
		html, err := getter.Get(ctx, t.URL)
		if err != nil {
			slog.Warn("photos: fetch detail page", "property", t.PropertyID, "url", t.URL, "err", err)
			// Mark fetched (with zero photos) so we don't retry a dead URL forever.
			_ = sc.store.SavePhotos(ctx, t.PropertyID, nil)
			continue
		}
		urls := scraper.ExtractDetailPhotos(html, site.Selectors)
		pics, _ := sc.photos.Download(ctx, t.PropertyID, urls, set.MaxPhotosPerListing, t.URL)
		if err := sc.store.SavePhotos(ctx, t.PropertyID, pics); err != nil {
			slog.Error("photos: save", "property", t.PropertyID, "err", err)
			continue
		}
		slog.Debug("photos: downloaded", "property", t.PropertyID, "found", len(urls), "saved", len(pics))
		total += len(pics)
		ok++
		if ctx.Err() != nil {
			break
		}
	}
	slog.Info("photos: run complete", "listings", ok, "photos", total, "took", time.Since(start).Round(time.Millisecond))
}

// resolveMetro resolves the nearest subway station for a batch of listings that
// don't have one yet (capped per run). Each unlocated listing is geocoded once
// via Nominatim (results cached), then matched against the embedded station
// network. Located listings (or confirmed failures) are marked so they aren't
// retried every pass.
func (sc *Scheduler) resolveMetro(ctx context.Context, set models.Settings) {
	targets, err := sc.store.PropertiesNeedingMetro(ctx, set.MaxMetroLookupsPerRun)
	if err != nil {
		slog.Error("metro: query targets", "err", err)
		return
	}
	if len(targets) == 0 {
		return
	}
	if more, _ := sc.store.PropertiesNeedingMetro(ctx, set.MaxMetroLookupsPerRun+1); len(more) > len(targets) {
		slog.Info("metro: capping this run", "limit", set.MaxMetroLookupsPerRun, "note", "remaining listings resolved on later runs")
	}

	start := time.Now()
	var ok int
	for _, t := range targets {
		if err := sc.resolveOneMetro(ctx, t); err != nil {
			slog.Warn("metro: resolve", "property", t.PropertyID, "err", err)
		} else {
			ok++
		}
		if ctx.Err() != nil {
			break
		}
	}
	slog.Info("metro: run complete", "resolved", ok, "of", len(targets), "took", time.Since(start).Round(time.Millisecond))
}

// resolveOneMetro locates a single listing (using its coordinates, or by
// geocoding its address) and stores its nearest station. A listing that cannot
// be located is still marked checked (with an empty station) so it isn't
// retried forever. A transient geocoding error is returned WITHOUT marking the
// listing, so it is retried on the next run.
func (sc *Scheduler) resolveOneMetro(ctx context.Context, t store.MetroTarget) error {
	lat, lon := t.Latitude, t.Longitude
	if lat == 0 || lon == 0 {
		q := metroQuery(t.Address, t.Neighborhood)
		if q == "" {
			return sc.store.SaveMetro(ctx, t.PropertyID, 0, 0, "", "", "", 0, 0, 0)
		}
		if clat, clon, found, cached := sc.store.GetGeocode(ctx, q); cached {
			if !found {
				return sc.store.SaveMetro(ctx, t.PropertyID, 0, 0, "", "", "", 0, 0, 0)
			}
			lat, lon = clat, clon
		} else {
			res, err := geocode.Query(ctx, sc.fetcher, q)
			if err != nil {
				return err // transient: leave unchecked so it retries next run
			}
			_ = sc.store.PutGeocode(ctx, q, res.Lat, res.Lon, res.Found)
			if !res.Found {
				return sc.store.SaveMetro(ctx, t.PropertyID, 0, 0, "", "", "", 0, 0, 0)
			}
			lat, lon = res.Lat, res.Lon
		}
	}
	st, dist, found := metro.Nearest(lat, lon)
	if !found {
		return sc.store.SaveMetro(ctx, t.PropertyID, lat, lon, "", "", "", 0, 0, 0)
	}
	return sc.store.SaveMetro(ctx, t.PropertyID, lat, lon, st.Name, st.Line, st.Color, dist, st.Lat, st.Lon)
}

// ResolveMetroByID resolves the nearest station for a single listing on demand
// (used when the detail view is opened before the background batch has reached
// it). It is a no-op if the listing was already resolved.
func (sc *Scheduler) ResolveMetroByID(ctx context.Context, id int64) error {
	t, ok, err := sc.store.MetroTargetByID(ctx, id)
	if err != nil || !ok {
		return err
	}
	return sc.resolveOneMetro(ctx, t)
}

// metroQuery builds a Nominatim search string from a listing's address and
// neighborhood, scoped to São Paulo. Returns "" when there's nothing to go on.
func metroQuery(address, neighborhood string) string {
	// Addresses can carry scraped junk (extra whitespace / newlines); keep only
	// the first line and collapse internal whitespace.
	addr := strings.TrimSpace(strings.SplitN(address, "\n", 2)[0])
	addr = strings.Join(strings.Fields(addr), " ")
	neigh := strings.Join(strings.Fields(neighborhood), " ")
	var parts []string
	if addr != "" {
		parts = append(parts, addr)
	}
	if neigh != "" && !strings.EqualFold(neigh, addr) {
		parts = append(parts, neigh)
	}
	if len(parts) == 0 {
		return ""
	}
	parts = append(parts, "São Paulo", "SP", "Brasil")
	return strings.Join(parts, ", ")
}

// detailGetter picks the fetcher for a listing's detail page. Browser-rendered
// or anti-bot sites need the headless browser for their detail pages too.
func (sc *Scheduler) detailGetter(site models.Site) scraper.PageGetter {
	if site.DetailJSRender || site.JSRender {
		return sc.browser
	}
	return sc.fetcher
}

// runSite scrapes one site and persists matching listings, returning the number
// of newly-added listings and the number of listings seen (after post-filter).
func (sc *Scheduler) runSite(ctx context.Context, site models.Site, f models.Filters) (added, seen int, err error) {
	props, err := scraper.Scrape(ctx, sc.getter(site), site, f)
	if err != nil {
		return 0, 0, err
	}
	for i := range props {
		p := props[i]
		if !passesFilter(p, f) {
			continue
		}
		seen++
		isNew, err := sc.store.UpsertProperty(ctx, &p)
		if err != nil {
			return added, seen, err
		}
		if isNew {
			added++
		}
	}
	return added, seen, nil
}

// getter returns the headless browser for JS-render sites, else the HTTP fetcher.
func (sc *Scheduler) getter(site models.Site) scraper.PageGetter {
	if site.JSRender {
		return sc.browser
	}
	return sc.fetcher
}

// passesFilter applies the global filters as a post-scrape guard, since not
// every site honours every URL placeholder.
func passesFilter(p models.Property, f models.Filters) bool {
	if f.MaxPrice > 0 && p.Price > f.MaxPrice {
		return false
	}
	if f.MinPrice > 0 && p.Price > 0 && p.Price < f.MinPrice {
		return false
	}
	if f.MinBedrooms > 0 && p.Bedrooms > 0 && p.Bedrooms < f.MinBedrooms {
		return false
	}
	if f.MinAreaM2 > 0 && p.AreaM2 > 0 && p.AreaM2 < f.MinAreaM2 {
		return false
	}
	if f.Neighborhood != "" {
		hay := strings.ToLower(p.Neighborhood + " " + p.Address)
		if !strings.Contains(hay, strings.ToLower(f.Neighborhood)) {
			return false
		}
	}
	return true
}

func formatMsg(sites, added int) string {
	return time.Now().Format("15:04:05") + " — scraped " +
		itoa(sites) + " site(s), " + itoa(added) + " new listing(s)"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
