// Package store persists sites, properties and settings in a local SQLite
// database (pure-Go driver, no CGO required).
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"ppsc/internal/models"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at path and runs migrations.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	// SQLite handles one writer at a time; keep a single connection to avoid
	// "database is locked" under the scheduler + UI writing concurrently.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS sites (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	name         TEXT NOT NULL,
	enabled      INTEGER NOT NULL DEFAULT 1,
	url_template TEXT NOT NULL,
	strategy     TEXT NOT NULL,
	js_render    INTEGER NOT NULL DEFAULT 0,
	selectors    TEXT NOT NULL DEFAULT '{}',
	max_pages    INTEGER NOT NULL DEFAULT 1,
	notes        TEXT NOT NULL DEFAULT '',
	last_run     TIMESTAMP,
	last_status  TEXT NOT NULL DEFAULT 'never',
	last_error   TEXT NOT NULL DEFAULT '',
	last_found   INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS properties (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	fingerprint   TEXT NOT NULL UNIQUE,
	site_id       INTEGER NOT NULL,
	site_name     TEXT NOT NULL DEFAULT '',
	title         TEXT NOT NULL DEFAULT '',
	url           TEXT NOT NULL DEFAULT '',
	image_url     TEXT NOT NULL DEFAULT '',
	price         INTEGER NOT NULL DEFAULT 0,
	address       TEXT NOT NULL DEFAULT '',
	neighborhood  TEXT NOT NULL DEFAULT '',
	bedrooms      INTEGER NOT NULL DEFAULT 0,
	bathrooms     INTEGER NOT NULL DEFAULT 0,
	parking_spots INTEGER NOT NULL DEFAULT 0,
	area_m2       INTEGER NOT NULL DEFAULT 0,
	description   TEXT NOT NULL DEFAULT '',
	status        TEXT NOT NULL DEFAULT 'new',
	first_seen    TIMESTAMP NOT NULL,
	last_seen     TIMESTAMP NOT NULL,
	photos_fetched INTEGER NOT NULL DEFAULT 0,
	photo_count    INTEGER NOT NULL DEFAULT 0,
	favorite      INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_properties_status ON properties(status);
CREATE INDEX IF NOT EXISTS idx_properties_last_seen ON properties(last_seen);

CREATE TABLE IF NOT EXISTS photos (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	property_id INTEGER NOT NULL,
	ordinal     INTEGER NOT NULL DEFAULT 0,
	source_url  TEXT NOT NULL DEFAULT '',
	local_path  TEXT NOT NULL DEFAULT '',
	UNIQUE(property_id, ordinal)
);
CREATE INDEX IF NOT EXISTS idx_photos_property ON photos(property_id);

CREATE TABLE IF NOT EXISTS settings (
	id    INTEGER PRIMARY KEY CHECK (id = 1),
	json  TEXT NOT NULL
);
`)
	if err != nil {
		return err
	}
	// Add columns introduced after the initial schema (idempotent for older DBs).
	s.addColumnIfMissing("sites", "js_render", "INTEGER NOT NULL DEFAULT 0")
	s.addColumnIfMissing("sites", "detail_js_render", "INTEGER NOT NULL DEFAULT 0")
	s.addColumnIfMissing("properties", "photos_fetched", "INTEGER NOT NULL DEFAULT 0")
	s.addColumnIfMissing("properties", "photo_count", "INTEGER NOT NULL DEFAULT 0")
	s.addColumnIfMissing("properties", "favorite", "INTEGER NOT NULL DEFAULT 0")
	// Indexes that depend on columns added above must come after the ALTERs,
	// so this also works against databases created before those columns existed.
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_properties_photos ON properties(photos_fetched)`); err != nil {
		return err
	}
	return nil
}

// addColumnIfMissing runs ALTER TABLE ADD COLUMN only when the column is absent,
// so upgrades work against databases created by an earlier version.
func (s *Store) addColumnIfMissing(table, column, decl string) {
	rows, err := s.db.Query("SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if rows.Scan(&name) == nil && name == column {
			return // already present
		}
	}
	_, _ = s.db.Exec("ALTER TABLE " + table + " ADD COLUMN " + column + " " + decl)
}

// ---- Sites ----

func (s *Store) ListSites(ctx context.Context) ([]models.Site, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,enabled,url_template,strategy,js_render,detail_js_render,selectors,max_pages,notes,last_run,last_status,last_error,last_found FROM sites ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Site
	for rows.Next() {
		st, err := scanSite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s *Store) GetSite(ctx context.Context, id int64) (models.Site, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,name,enabled,url_template,strategy,js_render,detail_js_render,selectors,max_pages,notes,last_run,last_status,last_error,last_found FROM sites WHERE id=?`, id)
	return scanSite(row)
}

type scanner interface{ Scan(dest ...any) error }

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func scanSite(sc scanner) (models.Site, error) {
	var st models.Site
	var selJSON string
	var enabled, jsRender, detailJS int
	var lastRun sql.NullTime
	if err := sc.Scan(&st.ID, &st.Name, &enabled, &st.URLTemplate, &st.Strategy, &jsRender, &detailJS, &selJSON, &st.MaxPages, &st.Notes, &lastRun, &st.LastStatus, &st.LastError, &st.LastFound); err != nil {
		return st, err
	}
	st.Enabled = enabled != 0
	st.JSRender = jsRender != 0
	st.DetailJSRender = detailJS != 0
	if lastRun.Valid {
		st.LastRun = lastRun.Time
	}
	_ = json.Unmarshal([]byte(selJSON), &st.Selectors)
	return st, nil
}

func (s *Store) SaveSite(ctx context.Context, st *models.Site) error {
	sel, _ := json.Marshal(st.Selectors)
	enabled := boolToInt(st.Enabled)
	jsRender := boolToInt(st.JSRender)
	detailJS := boolToInt(st.DetailJSRender)
	if st.MaxPages < 1 {
		st.MaxPages = 1
	}
	if st.ID == 0 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO sites(name,enabled,url_template,strategy,js_render,detail_js_render,selectors,max_pages,notes,last_status) VALUES(?,?,?,?,?,?,?,?,?,'never')`,
			st.Name, enabled, st.URLTemplate, st.Strategy, jsRender, detailJS, string(sel), st.MaxPages, st.Notes)
		if err != nil {
			return err
		}
		st.ID, _ = res.LastInsertId()
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE sites SET name=?,enabled=?,url_template=?,strategy=?,js_render=?,detail_js_render=?,selectors=?,max_pages=?,notes=? WHERE id=?`,
		st.Name, enabled, st.URLTemplate, st.Strategy, jsRender, detailJS, string(sel), st.MaxPages, st.Notes, st.ID)
	return err
}

func (s *Store) DeleteSite(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sites WHERE id=?`, id)
	return err
}

// UpdateSiteRun records the outcome of a scrape run for a site.
func (s *Store) UpdateSiteRun(ctx context.Context, id int64, status, errMsg string, found int, when time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sites SET last_run=?,last_status=?,last_error=?,last_found=? WHERE id=?`,
		when, status, errMsg, found, id)
	return err
}

func (s *Store) CountSites(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sites`).Scan(&n)
	return n, err
}

// ---- Properties ----

// UpsertProperty inserts a new property or refreshes last_seen on an existing
// one (matched by fingerprint). Returns true when the property was newly added.
func (s *Store) UpsertProperty(ctx context.Context, p *models.Property) (bool, error) {
	now := time.Now()
	res, err := s.db.ExecContext(ctx, `
INSERT INTO properties(fingerprint,site_id,site_name,title,url,image_url,price,address,neighborhood,bedrooms,bathrooms,parking_spots,area_m2,description,status,first_seen,last_seen)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?, 'new', ?, ?)
ON CONFLICT(fingerprint) DO UPDATE SET
	last_seen=excluded.last_seen,
	price=excluded.price,
	title=CASE WHEN excluded.title<>'' THEN excluded.title ELSE properties.title END,
	image_url=CASE WHEN excluded.image_url<>'' THEN excluded.image_url ELSE properties.image_url END`,
		p.Fingerprint, p.SiteID, p.SiteName, p.Title, p.URL, p.ImageURL, p.Price, p.Address, p.Neighborhood,
		p.Bedrooms, p.Bathrooms, p.ParkingSpots, p.AreaM2, p.Description, now, now)
	if err != nil {
		return false, err
	}
	// rows affected is 1 on insert, 2 on update (with ON CONFLICT) under SQLite.
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// PropertyQuery describes filtering/sorting for ListProperties.
type PropertyQuery struct {
	Status       string
	MinPrice     int64
	MaxPrice     int64
	MinBedrooms  int
	MinAreaM2    int
	Neighborhood string
	Search        string
	Sort          string // newest | price_asc | price_desc
	FavoritesOnly bool
	Limit         int
}

func (s *Store) ListProperties(ctx context.Context, q PropertyQuery) ([]models.Property, error) {
	sb := `SELECT id,fingerprint,site_id,site_name,title,url,image_url,price,address,neighborhood,bedrooms,bathrooms,parking_spots,area_m2,description,status,first_seen,last_seen,photos_fetched,photo_count,favorite,
		(SELECT local_path FROM photos WHERE photos.property_id=properties.id ORDER BY ordinal LIMIT 1) AS thumb_path
		FROM properties WHERE 1=1`
	var args []any
	if q.Status != "" && q.Status != "all" {
		sb += ` AND status=?`
		args = append(args, q.Status)
	} else {
		sb += ` AND status<>'hidden'`
	}
	if q.FavoritesOnly {
		sb += ` AND favorite=1`
	}
	if q.MinPrice > 0 {
		sb += ` AND price>=?`
		args = append(args, q.MinPrice)
	}
	if q.MaxPrice > 0 {
		sb += ` AND (price<=? AND price>0)`
		args = append(args, q.MaxPrice)
	}
	if q.MinBedrooms > 0 {
		sb += ` AND bedrooms>=?`
		args = append(args, q.MinBedrooms)
	}
	if q.MinAreaM2 > 0 {
		sb += ` AND area_m2>=?`
		args = append(args, q.MinAreaM2)
	}
	if q.Neighborhood != "" {
		sb += ` AND (neighborhood LIKE ? OR address LIKE ?)`
		args = append(args, "%"+q.Neighborhood+"%", "%"+q.Neighborhood+"%")
	}
	if q.Search != "" {
		sb += ` AND (title LIKE ? OR description LIKE ? OR address LIKE ?)`
		args = append(args, "%"+q.Search+"%", "%"+q.Search+"%", "%"+q.Search+"%")
	}
	switch q.Sort {
	case "price_asc":
		sb += ` ORDER BY (price=0), price ASC`
	case "price_desc":
		sb += ` ORDER BY price DESC`
	default:
		sb += ` ORDER BY first_seen DESC`
	}
	if q.Limit <= 0 {
		q.Limit = 500
	}
	sb += ` LIMIT ?`
	args = append(args, q.Limit)

	rows, err := s.db.QueryContext(ctx, sb, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Property
	for rows.Next() {
		var p models.Property
		var photosFetched, favorite int
		var thumb sql.NullString
		if err := rows.Scan(&p.ID, &p.Fingerprint, &p.SiteID, &p.SiteName, &p.Title, &p.URL, &p.ImageURL, &p.Price,
			&p.Address, &p.Neighborhood, &p.Bedrooms, &p.Bathrooms, &p.ParkingSpots, &p.AreaM2, &p.Description,
			&p.Status, &p.FirstSeen, &p.LastSeen, &photosFetched, &p.PhotoCount, &favorite, &thumb); err != nil {
			return nil, err
		}
		p.PhotosFetched = photosFetched != 0
		p.Favorite = favorite != 0
		p.ThumbPath = thumb.String
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) SetPropertyStatus(ctx context.Context, id int64, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE properties SET status=? WHERE id=?`, status, id)
	return err
}

func (s *Store) SetFavorite(ctx context.Context, id int64, favorite bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE properties SET favorite=? WHERE id=?`, boolToInt(favorite), id)
	return err
}

// ---- Photos ----

// PhotoTarget identifies a listing whose detail page still needs to be visited
// for photos, along with the URL and site needed to fetch it.
type PhotoTarget struct {
	PropertyID int64
	URL        string
	SiteID     int64
}

// PropertiesNeedingPhotos returns up to limit not-hidden listings that have a
// URL and have not had their photos fetched yet (newest first).
func (s *Store) PropertiesNeedingPhotos(ctx context.Context, limit int) ([]PhotoTarget, error) {
	if limit <= 0 {
		limit = 25
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,url,site_id FROM properties
		WHERE photos_fetched=0 AND url<>'' AND status<>'hidden'
		ORDER BY first_seen DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PhotoTarget
	for rows.Next() {
		var t PhotoTarget
		if err := rows.Scan(&t.PropertyID, &t.URL, &t.SiteID); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SavePhotos records downloaded photos for a property, marks it fetched, and
// updates its photo_count. Replaces any existing rows for the property.
func (s *Store) SavePhotos(ctx context.Context, propertyID int64, photos []models.Photo) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM photos WHERE property_id=?`, propertyID); err != nil {
		return err
	}
	for i, p := range photos {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO photos(property_id,ordinal,source_url,local_path) VALUES(?,?,?,?)`,
			propertyID, i, p.SourceURL, p.LocalPath); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE properties SET photos_fetched=1, photo_count=? WHERE id=?`,
		len(photos), propertyID); err != nil {
		return err
	}
	return tx.Commit()
}

// GetPhotos returns a property's downloaded photos in order.
func (s *Store) GetPhotos(ctx context.Context, propertyID int64) ([]models.Photo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,property_id,ordinal,source_url,local_path FROM photos WHERE property_id=? ORDER BY ordinal`, propertyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Photo
	for rows.Next() {
		var p models.Photo
		if err := rows.Scan(&p.ID, &p.PropertyID, &p.Ordinal, &p.SourceURL, &p.LocalPath); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Stats are headline counts shown on the dashboard.
type Stats struct {
	Total     int `json:"total"`
	New       int `json:"new"`
	Favorites int `json:"favorites"`
	Sites     int `json:"sites"`
}

func (s *Store) Stats(ctx context.Context) (Stats, error) {
	var st Stats
	row := s.db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM properties WHERE status<>'hidden'),
		(SELECT COUNT(*) FROM properties WHERE status='new'),
		(SELECT COUNT(*) FROM properties WHERE favorite=1),
		(SELECT COUNT(*) FROM sites WHERE enabled=1)`)
	err := row.Scan(&st.Total, &st.New, &st.Favorites, &st.Sites)
	return st, err
}

// ---- Settings ----

func (s *Store) GetSettings(ctx context.Context) (models.Settings, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT json FROM settings WHERE id=1`).Scan(&raw)
	if err == sql.ErrNoRows {
		return defaultSettings(), nil
	}
	if err != nil {
		return models.Settings{}, err
	}
	var set models.Settings
	_ = json.Unmarshal([]byte(raw), &set)
	// Settings rows saved before the photo feature existed lack the
	// "download_photos" key entirely, which would silently unmarshal to false
	// and disable photo downloads forever. Treat an absent key as the intended
	// default (on); only an explicit "download_photos":false stays off.
	var probe map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &probe) == nil {
		if _, ok := probe["download_photos"]; !ok {
			set.DownloadPhotos = true
		}
	}
	if set.RequestDelaySeconds <= 0 {
		set.RequestDelaySeconds = 5 // sane, polite default for older saved settings
	}
	if set.MaxPhotosPerListing <= 0 {
		set.MaxPhotosPerListing = 20
	}
	if set.MaxPhotoFetchesPerRun <= 0 {
		set.MaxPhotoFetchesPerRun = 25
	}
	return set, nil
}

func defaultSettings() models.Settings {
	return models.Settings{
		IntervalMinutes:       360,
		RequestDelaySeconds:   5,
		DownloadPhotos:        true,
		MaxPhotosPerListing:   20,
		MaxPhotoFetchesPerRun: 25,
	}
}

func (s *Store) SaveSettings(ctx context.Context, set models.Settings) error {
	raw, _ := json.Marshal(set)
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings(id,json) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET json=excluded.json`, string(raw))
	return err
}
