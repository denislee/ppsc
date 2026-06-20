// Package server exposes the JSON API and serves the embedded web UI.
package server

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"strconv"
	"time"

	"ppsc/internal/models"
	"ppsc/internal/scheduler"
	"ppsc/internal/scraper"
	"ppsc/internal/store"
)

type Server struct {
	store    *store.Store
	sched    *scheduler.Scheduler
	fetcher  *scraper.Fetcher
	browser  *scraper.BrowserFetcher
	web      fs.FS
	photoDir string
}

func New(s *store.Store, sc *scheduler.Scheduler, f *scraper.Fetcher, b *scraper.BrowserFetcher, web fs.FS, photoDir string) *Server {
	return &Server{store: s, sched: sc, fetcher: f, browser: b, web: web, photoDir: photoDir}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("POST /api/scrape", s.handleScrape)

	mux.HandleFunc("GET /api/properties", s.handleListProperties)
	mux.HandleFunc("GET /api/cities", s.handleListCities)
	mux.HandleFunc("GET /api/neighborhoods", s.handleListNeighborhoods)
	mux.HandleFunc("POST /api/properties/{id}/status", s.handleSetStatus)
	mux.HandleFunc("POST /api/properties/{id}/favorite", s.handleSetFavorite)
	mux.HandleFunc("GET /api/properties/{id}/photos", s.handlePropertyPhotos)
	mux.HandleFunc("GET /api/properties/{id}/metro", s.handlePropertyMetro)
	mux.HandleFunc("POST /api/metro/reresolve", s.handleReResolveMetro)

	// Serve downloaded photos from disk at /photos/<id>/NN.jpg.
	mux.Handle("GET /photos/", http.StripPrefix("/photos/", http.FileServer(http.Dir(s.photoDir))))

	mux.HandleFunc("GET /api/sites", s.handleListSites)
	mux.HandleFunc("POST /api/sites", s.handleSaveSite)
	mux.HandleFunc("DELETE /api/sites/{id}", s.handleDeleteSite)
	mux.HandleFunc("POST /api/sites/test", s.handleTestSite)

	mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	mux.HandleFunc("POST /api/settings", s.handleSaveSettings)

	mux.Handle("GET /", http.FileServerFS(s.web))
	return mux
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func ctxOf(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 90*time.Second)
}

// ---- status / scrape ----

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := ctxOf(r)
	defer cancel()
	stats, err := s.store.Stats(ctx)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	state, _ := s.store.GetScrapeState(ctx)
	sites, _ := s.store.ListSites(ctx)
	done := make(map[int64]bool, len(state.DoneSites))
	for _, id := range state.DoneSites {
		done[id] = true
	}
	var enabled, pending int
	for _, st := range sites {
		if st.Enabled {
			enabled++
			if !done[st.ID] {
				pending++
			}
		}
	}
	writeJSON(w, 200, map[string]any{
		"stats":     stats,
		"scheduler": s.sched.Status(),
		"scrape": map[string]any{
			"status":     state.Status,
			"resumable":  state.Status == "interrupted",
			"pending":    pending, // enabled sites not yet done this run
			"done":       enabled - pending,
			"total":      enabled,
			"started_at": state.StartedAt,
		},
	})
}

func (s *Server) handleScrape(w http.ResponseWriter, r *http.Request) {
	resume := r.URL.Query().Get("mode") == "resume"
	// Run in the background so the HTTP request returns immediately. The timeout
	// is generous because a full pass across all sites' pages can be lengthy.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		_, _ = s.sched.Run(ctx, resume)
	}()
	writeJSON(w, 202, map[string]string{"status": "started"})
}

// ---- properties ----

func (s *Server) handleListProperties(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := ctxOf(r)
	defer cancel()
	q := r.URL.Query()
	pq := store.PropertyQuery{
		Status:        q.Get("status"),
		MinPrice:      atoi64(q.Get("min_price")),
		MaxPrice:      atoi64(q.Get("max_price")),
		MinBedrooms:   atoi(q.Get("min_beds")),
		MinAreaM2:     atoi(q.Get("min_area")),
		Neighborhood:  q.Get("neighborhood"),
		City:          q.Get("city"),
		Search:        q.Get("q"),
		Sort:          q.Get("sort"),
		FavoritesOnly: q.Get("favorites") == "1",
	}
	props, err := s.store.ListProperties(ctx, pq)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	if props == nil {
		props = []models.Property{}
	}
	writeJSON(w, 200, props)
}

// handleListCities returns the distinct municipalities seen across listings,
// for the city filter dropdown.
func (s *Server) handleListCities(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := ctxOf(r)
	defer cancel()
	cities, err := s.store.ListCities(ctx)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	if cities == nil {
		cities = []string{}
	}
	writeJSON(w, 200, cities)
}

// handleListNeighborhoods returns the distinct neighborhoods seen across
// listings, for the neighborhood filter dropdown.
func (s *Server) handleListNeighborhoods(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := ctxOf(r)
	defer cancel()
	neighborhoods, err := s.store.ListNeighborhoods(ctx)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	if neighborhoods == nil {
		neighborhoods = []string{}
	}
	writeJSON(w, 200, neighborhoods)
}

func (s *Server) handleSetStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := ctxOf(r)
	defer cancel()
	id := atoi64(r.PathValue("id"))
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, 400, "bad body")
		return
	}
	switch body.Status {
	case "new", "seen", "saved", "hidden":
	default:
		httpErr(w, 400, "invalid status")
		return
	}
	if err := s.store.SetPropertyStatus(ctx, id, body.Status); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) handleSetFavorite(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := ctxOf(r)
	defer cancel()
	var body struct {
		Favorite bool `json:"favorite"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, 400, "bad body")
		return
	}
	if err := s.store.SetFavorite(ctx, atoi64(r.PathValue("id")), body.Favorite); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handlePropertyPhotos returns a listing's photos. If they haven't been fetched
// yet (the background batch hasn't reached this listing), it scrapes the detail
// page on demand first. Fetching hits the live site through the throttled
// getter — and may spin up a headless browser — so allow a generous timeout.
func (s *Server) handlePropertyPhotos(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	id := atoi64(r.PathValue("id"))
	if err := s.sched.FetchPhotosByID(ctx, id); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	pics, err := s.store.GetPhotos(ctx, id)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	if pics == nil {
		pics = []models.Photo{}
	}
	writeJSON(w, 200, pics)
}

// handlePropertyMetro resolves (if needed) and returns the listing's
// nearest-station info: which station, its line/colour, the distance, and the
// coordinates of both the property and the station (for drawing the map). The
// lookup geocodes via Nominatim on first access, so allow a generous timeout.
func (s *Server) handlePropertyMetro(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	id := atoi64(r.PathValue("id"))
	if err := s.sched.ResolveMetroByID(ctx, id); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	p, ok, err := s.store.GetProperty(ctx, id)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	if !ok {
		httpErr(w, 404, "not found")
		return
	}
	writeJSON(w, 200, map[string]any{
		"checked":    p.MetroChecked,
		"city":       p.City,
		"station":    p.MetroStation,
		"line":       p.MetroLine,
		"color":      p.MetroColor,
		"distance_m": p.MetroDistanceM,
		"property":   map[string]float64{"lat": p.Latitude, "lon": p.Longitude},
		"metro":      map[string]float64{"lat": p.MetroLat, "lon": p.MetroLon},
	})
}

// handleReResolveMetro repairs listings scraped before recent improvements: it
// first cleans stored neighborhoods that were captured as descriptive prose
// (which also unblocks their geocoding), then re-queues listings that were
// checked but never located (no coordinates, no station) and resolves them in
// the background using the improved geocoding. Returns how many neighborhoods
// were cleaned and listings re-queued; the resolution continues after the
// response.
func (s *Server) handleReResolveMetro(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := ctxOf(r)
	defer cancel()
	cleaned, err := s.store.CleanStoredNeighborhoods(ctx, scraper.CleanNeighborhood)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	n, err := s.store.ResetUnlocatedMetro(ctx)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	if n > 0 {
		// Resolving geocodes politely (~1 req/s), so do it in the background and
		// return immediately, like POST /api/scrape.
		go func() {
			bg, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()
			_, _ = s.sched.ResolvePendingMetro(bg)
		}()
	}
	writeJSON(w, 202, map[string]int{"cleaned": cleaned, "reset": n})
}

// ---- sites ----

func (s *Server) handleListSites(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := ctxOf(r)
	defer cancel()
	sites, err := s.store.ListSites(ctx)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	if sites == nil {
		sites = []models.Site{}
	}
	writeJSON(w, 200, sites)
}

func (s *Server) handleSaveSite(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := ctxOf(r)
	defer cancel()
	var site models.Site
	if err := json.NewDecoder(r.Body).Decode(&site); err != nil {
		httpErr(w, 400, "bad body")
		return
	}
	if site.Name == "" || site.URLTemplate == "" {
		httpErr(w, 400, "name and url_template are required")
		return
	}
	if site.Strategy != models.StrategyCSS && site.Strategy != models.StrategyNextData {
		site.Strategy = models.StrategyCSS
	}
	if err := s.store.SaveSite(ctx, &site); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, site)
}

func (s *Server) handleDeleteSite(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := ctxOf(r)
	defer cancel()
	if err := s.store.DeleteSite(ctx, atoi64(r.PathValue("id"))); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleTestSite runs a site config live (first page only) without persisting,
// so the user can iterate on selectors from the UI.
func (s *Server) handleTestSite(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	var site models.Site
	if err := json.NewDecoder(r.Body).Decode(&site); err != nil {
		httpErr(w, 400, "bad body")
		return
	}
	site.MaxPages = 1
	set, _ := s.store.GetSettings(ctx)
	delay := time.Duration(set.RequestDelaySeconds) * time.Second
	var getter scraper.PageGetter = s.fetcher
	if site.JSRender {
		getter = s.browser
	}
	getter.SetMinInterval(delay)
	props, err := scraper.Scrape(ctx, getter, site, set.Filters)
	resp := map[string]any{"count": len(props)}
	if err != nil {
		resp["error"] = err.Error()
	}
	if len(props) > 8 {
		props = props[:8]
	}
	if props == nil {
		props = []models.Property{}
	}
	resp["sample"] = props
	writeJSON(w, 200, resp)
}

// ---- settings ----

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := ctxOf(r)
	defer cancel()
	set, err := s.store.GetSettings(ctx)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, set)
}

func (s *Server) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := ctxOf(r)
	defer cancel()
	var set models.Settings
	if err := json.NewDecoder(r.Body).Decode(&set); err != nil {
		httpErr(w, 400, "bad body")
		return
	}
	if err := s.store.SaveSettings(ctx, set); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	s.sched.Kick() // pick up new interval immediately
	writeJSON(w, 200, set)
}

func atoi(s string) int     { v, _ := strconv.Atoi(s); return v }
func atoi64(s string) int64 { v, _ := strconv.ParseInt(s, 10, 64); return v }
