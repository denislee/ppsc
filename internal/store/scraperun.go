package store

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"time"
)

// ScrapeState is the persisted progress of a scrape pass. It lets an abruptly
// stopped run (process killed mid-pass) be resumed — continuing the sites not
// yet completed — or started over from scratch.
type ScrapeState struct {
	Status    string    `json:"status"`     // "idle" | "running" | "interrupted"
	StartedAt time.Time `json:"started_at"` // when the current/last run began
	DoneSites []int64   `json:"done_sites"` // site IDs completed in the current run
}

// GetScrapeState returns the persisted scrape progress (idle/empty if none).
func (s *Store) GetScrapeState(ctx context.Context) (ScrapeState, error) {
	var (
		st      ScrapeState
		started sql.NullTime
		done    string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT status, started_at, done_sites FROM scrape_state WHERE id=1`).
		Scan(&st.Status, &started, &done)
	if err == sql.ErrNoRows {
		return ScrapeState{Status: "idle"}, nil
	}
	if err != nil {
		return ScrapeState{}, err
	}
	if started.Valid {
		st.StartedAt = started.Time
	}
	st.DoneSites = parseIDs(done)
	return st, nil
}

// BeginScrape marks a pass as running and returns the set of site IDs already
// completed. When resume is false it starts fresh (clears progress, stamps a new
// start time); when true it keeps the prior progress so the caller can skip
// sites that were already done before the interruption.
func (s *Store) BeginScrape(ctx context.Context, resume bool) ([]int64, error) {
	if resume {
		st, err := s.GetScrapeState(ctx)
		if err != nil {
			return nil, err
		}
		// Re-arm the row as running while preserving started_at/done_sites.
		_, err = s.db.ExecContext(ctx, `UPDATE scrape_state SET status='running' WHERE id=1`)
		return st.DoneSites, err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO scrape_state(id,status,started_at,done_sites) VALUES(1,'running',?,'')
		ON CONFLICT(id) DO UPDATE SET status='running', started_at=excluded.started_at, done_sites=''`,
		time.Now())
	return nil, err
}

// MarkSiteScraped records that a site finished in the current run, so a later
// resume skips it.
func (s *Store) MarkSiteScraped(ctx context.Context, id int64) error {
	st, err := s.GetScrapeState(ctx)
	if err != nil {
		return err
	}
	for _, existing := range st.DoneSites {
		if existing == id {
			return nil
		}
	}
	st.DoneSites = append(st.DoneSites, id)
	_, err = s.db.ExecContext(ctx, `UPDATE scrape_state SET done_sites=? WHERE id=1`, formatIDs(st.DoneSites))
	return err
}

// FinishScrape marks the pass complete and clears its per-site progress.
func (s *Store) FinishScrape(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO scrape_state(id,status,done_sites) VALUES(1,'idle','')
		ON CONFLICT(id) DO UPDATE SET status='idle', done_sites=''`)
	return err
}

// MarkScrapeInterrupted flags the pass as interrupted while keeping its progress,
// so it can be resumed. Used on a graceful mid-run cancellation.
func (s *Store) MarkScrapeInterrupted(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE scrape_state SET status='interrupted' WHERE id=1 AND status='running'`)
	return err
}

// MarkInterruptedIfRunning promotes a 'running' row left behind by a crashed
// process to 'interrupted' at startup, and reports whether it did. The progress
// (done_sites) is preserved so the run can be resumed from the UI.
func (s *Store) MarkInterruptedIfRunning(ctx context.Context) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE scrape_state SET status='interrupted' WHERE id=1 AND status='running'`)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func parseIDs(csv string) []int64 {
	if strings.TrimSpace(csv) == "" {
		return nil
	}
	var ids []int64
	for _, p := range strings.Split(csv, ",") {
		if n, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64); err == nil {
			ids = append(ids, n)
		}
	}
	return ids
}

func formatIDs(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, ",")
}
