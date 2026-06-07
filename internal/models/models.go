// Package models defines the core data types shared across the application.
package models

import "time"

// Property is a single real-estate listing discovered by a scraper.
type Property struct {
	ID int64 `json:"id"`
	// Fingerprint is a stable hash used to deduplicate the same listing
	// across repeated scrapes (and, where possible, across sites).
	Fingerprint string    `json:"fingerprint"`
	SiteID      int64     `json:"site_id"`
	SiteName    string    `json:"site_name"`
	Title       string    `json:"title"`
	URL         string    `json:"url"`
	ImageURL    string    `json:"image_url"`
	Price       int64     `json:"price"` // in BRL, whole reais; 0 means unknown
	Address     string    `json:"address"`
	Neighborhood string   `json:"neighborhood"`
	Bedrooms    int       `json:"bedrooms"`
	Bathrooms   int       `json:"bathrooms"`
	ParkingSpots int      `json:"parking_spots"`
	AreaM2      int       `json:"area_m2"`
	Description string    `json:"description"`
	// Status lets the user triage listings from the UI.
	Status string `json:"status"` // new | seen | hidden
	// Favorite is an independent "liked" tag, separate from Status.
	Favorite  bool      `json:"favorite"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`

	// PhotosFetched is set once the detail page has been visited for photos
	// (so it is not re-fetched on every scrape). PhotoCount is the number of
	// downloaded photos. Photos is populated on demand by the API.
	PhotosFetched bool    `json:"photos_fetched"`
	PhotoCount    int     `json:"photo_count"`
	Photos        []Photo `json:"photos,omitempty"`
	// ThumbPath is the local path (under the photo dir) of the first
	// downloaded photo, if any. The UI prefers it for the card thumbnail since
	// it is served locally from /photos and always loads — unlike the scraped
	// ImageURL, which is often absent (lazy-loaded list pages) or hotlinked.
	ThumbPath string `json:"thumb_path,omitempty"`
}

// Photo is a single downloaded image belonging to a Property.
type Photo struct {
	ID         int64  `json:"id"`
	PropertyID int64  `json:"property_id"`
	Ordinal    int    `json:"ordinal"`
	SourceURL  string `json:"source_url"`
	// LocalPath is the path relative to the photo directory, e.g. "42/03.jpg".
	// The UI loads it from /photos/<LocalPath>.
	LocalPath string `json:"local_path"`
}

// Strategy identifies how a Site's pages are parsed into Property records.
type Strategy string

const (
	// StrategyCSS parses server-rendered HTML using goquery CSS selectors.
	StrategyCSS Strategy = "css"
	// StrategyNextData extracts the embedded __NEXT_DATA__ JSON blob that
	// Next.js sites (VivaReal, ZAP, OLX, QuintoAndar, ...) ship in the page
	// and maps fields via dotted/indexed key paths.
	StrategyNextData Strategy = "nextdata"
)

// Selectors maps Property fields to either CSS selectors (StrategyCSS) or
// JSON key paths (StrategyNextData). Empty values are simply skipped.
type Selectors struct {
	// Item is the selector/path that yields the repeating listing element.
	// For CSS it is a goquery selector; for nextdata it is a dotted path to
	// a JSON array, e.g. "props.pageProps.results.listings".
	Item string `json:"item"`

	Title        string `json:"title"`
	URL          string `json:"url"`
	URLPrefix    string `json:"url_prefix"` // prepended to relative URLs
	Image        string `json:"image"`
	Price        string `json:"price"`
	Address      string `json:"address"`
	Neighborhood string `json:"neighborhood"`
	Bedrooms     string `json:"bedrooms"`
	Bathrooms    string `json:"bathrooms"`
	ParkingSpots string `json:"parking_spots"`
	AreaM2       string `json:"area_m2"`
	Description  string `json:"description"`

	// AttrURL / AttrImage name the HTML attribute to read for CSS strategy
	// (e.g. "href", "src", "data-src"). Defaults applied if empty.
	AttrURL   string `json:"attr_url"`
	AttrImage string `json:"attr_image"`

	// DetailPhotos is an optional CSS selector matching the gallery <img>
	// elements on a listing's DETAIL page (e.g. `[data-testid="gallery"] img`).
	// When empty, photos are discovered generically (og:image + JSON-LD). The
	// detail page is fetched once per new listing to collect the full gallery.
	DetailPhotos    string `json:"detail_photos"`
	DetailPhotoAttr string `json:"detail_photo_attr"` // attr to read; default tries src/data-src/srcset
	// PhotoPrefix is prepended to relative photo URLs (CDN base).
	PhotoPrefix string `json:"photo_prefix"`
}

// Site is a configurable scrape target. Users create and edit these from the UI.
type Site struct {
	ID      int64    `json:"id"`
	Name    string   `json:"name"`
	Enabled bool     `json:"enabled"`
	// URLTemplate is the search URL with placeholders substituted at scrape
	// time: {query} {minPrice} {maxPrice} {minBeds} {neighborhood} {page}.
	URLTemplate string   `json:"url_template"`
	Strategy    Strategy `json:"strategy"`
	// JSRender fetches the page with a headless browser instead of plain HTTP —
	// needed for JS-rendered listings and many anti-bot-protected sites.
	JSRender bool `json:"js_render"`
	// DetailJSRender fetches each listing's DETAIL page (for photos) with the
	// headless browser. Defaults to JSRender when unset is fine in practice.
	DetailJSRender bool      `json:"detail_js_render"`
	Selectors      Selectors `json:"selectors"`
	// MaxPages limits pagination per run (1 = only the first page).
	MaxPages int `json:"max_pages"`
	// Notes is free text shown in the UI (e.g. caveats about the site).
	Notes string `json:"notes"`

	LastRun     time.Time `json:"last_run"`
	LastStatus  string    `json:"last_status"`  // ok | error | never
	LastError   string    `json:"last_error"`
	LastFound   int       `json:"last_found"`
}

// Filters are the global search criteria applied across every enabled site.
// They feed the URLTemplate placeholders and also post-filter results.
type Filters struct {
	Query        string `json:"query"`
	MinPrice     int64  `json:"min_price"`
	MaxPrice     int64  `json:"max_price"`
	MinBedrooms  int    `json:"min_bedrooms"`
	MinAreaM2    int    `json:"min_area_m2"`
	Neighborhood string `json:"neighborhood"`
}

// Settings holds global, app-wide configuration.
type Settings struct {
	Filters         Filters `json:"filters"`
	IntervalMinutes int     `json:"interval_minutes"` // 0 disables the scheduler
	// RequestDelaySeconds is the minimum gap between requests to the same host,
	// to stay polite and avoid tripping rate limits / anti-scraper defences.
	RequestDelaySeconds int `json:"request_delay_seconds"`

	// DownloadPhotos enables fetching each new listing's detail page and
	// downloading its photo gallery to local disk.
	DownloadPhotos bool `json:"download_photos"`
	// MaxPhotosPerListing caps how many images are downloaded per listing.
	MaxPhotosPerListing int `json:"max_photos_per_listing"`
	// MaxPhotoFetchesPerRun caps how many detail pages are visited for photos
	// in a single scrape pass (the rest are picked up on later runs).
	MaxPhotoFetchesPerRun int `json:"max_photo_fetches_per_run"`
}
