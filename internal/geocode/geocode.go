// Package geocode resolves a free-text address to coordinates using the
// OpenStreetMap Nominatim service (no API key required). It does not fetch
// pages itself — it borrows the caller's polite, rate-limited PageGetter so
// requests to Nominatim honour the same per-host throttle as scraping, well
// within Nominatim's usage policy (max 1 request/second).
package geocode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
)

// Getter fetches a URL's body. Satisfied by scraper.Fetcher.
type Getter interface {
	Get(ctx context.Context, rawURL string) (string, error)
}

// Result is a geocoding outcome. Found is false when Nominatim returned no
// match (a legitimate result worth caching so we don't re-query a dead address).
type Result struct {
	Lat   float64
	Lon   float64
	City  string // resolved municipality, e.g. "São Paulo" (best effort, may be "")
	Found bool
}

// Query geocodes a free-text address, biased to Brazil. An empty query returns
// a not-found result without hitting the network.
func Query(ctx context.Context, g Getter, query string) (Result, error) {
	if query == "" {
		return Result{}, nil
	}
	u := "https://nominatim.openstreetmap.org/search?" + url.Values{
		"q":              {query},
		"format":         {"jsonv2"},
		"limit":          {"1"},
		"countrycodes":   {"br"},
		"addressdetails": {"1"},
	}.Encode()

	body, err := g.Get(ctx, u)
	if err != nil {
		return Result{}, fmt.Errorf("geocode %q: %w", query, err)
	}
	var hits []struct {
		Lat     string `json:"lat"`
		Lon     string `json:"lon"`
		Address struct {
			City         string `json:"city"`
			Town         string `json:"town"`
			Municipality string `json:"municipality"`
			Village      string `json:"village"`
			County       string `json:"county"`
		} `json:"address"`
	}
	if err := json.Unmarshal([]byte(body), &hits); err != nil {
		return Result{}, fmt.Errorf("geocode %q: decode: %w", query, err)
	}
	if len(hits) == 0 {
		return Result{Found: false}, nil
	}
	lat, err1 := strconv.ParseFloat(hits[0].Lat, 64)
	lon, err2 := strconv.ParseFloat(hits[0].Lon, 64)
	if err1 != nil || err2 != nil {
		return Result{}, fmt.Errorf("geocode %q: bad coordinates", query)
	}
	a := hits[0].Address
	city := firstNonEmpty(a.City, a.Town, a.Municipality, a.Village, a.County)
	return Result{Lat: lat, Lon: lon, City: city, Found: true}, nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
