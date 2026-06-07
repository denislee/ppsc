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
	mux.HandleFunc("POST /api/properties/{id}/status", s.handleSetStatus)
	mux.HandleFunc("POST /api/properties/{id}/favorite", s.handleSetFavorite)
	mux.HandleFunc("GET /api/properties/{id}/photos", s.handlePropertyPhotos)

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
	writeJSON(w, 200, map[string]any{
		"stats":     stats,
		"scheduler": s.sched.Status(),
	})
}

func (s *Server) handleScrape(w http.ResponseWriter, r *http.Request) {
	// Run in the background so the HTTP request returns immediately.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		_, _ = s.sched.RunAll(ctx)
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

func (s *Server) handlePropertyPhotos(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := ctxOf(r)
	defer cancel()
	pics, err := s.store.GetPhotos(ctx, atoi64(r.PathValue("id")))
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	if pics == nil {
		pics = []models.Photo{}
	}
	writeJSON(w, 200, pics)
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

func atoi(s string) int       { v, _ := strconv.Atoi(s); return v }
func atoi64(s string) int64   { v, _ := strconv.ParseInt(s, 10, 64); return v }
