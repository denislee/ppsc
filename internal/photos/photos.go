// Package photos downloads listing images to local disk.
package photos

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ppsc/internal/models"
)

// Manager downloads images into a per-property folder under Dir.
type Manager struct {
	Dir    string
	client *http.Client
}

func NewManager(dir string) *Manager {
	return &Manager{
		Dir:    dir,
		client: &http.Client{Timeout: 45 * time.Second},
	}
}

const maxImageBytes = 15 << 20 // 15 MiB per image

// Download fetches up to max images for a property, writing them to
// <Dir>/<propertyID>/NN.<ext>, and returns the saved photo records. referer is
// the listing URL (some CDNs reject hotlinks without it). Individual failures
// are skipped; the call only errors if the directory can't be created.
func (m *Manager) Download(ctx context.Context, propertyID int64, urls []string, max int, referer string) ([]models.Photo, error) {
	if max > 0 && len(urls) > max {
		urls = urls[:max]
	}
	dir := filepath.Join(m.Dir, strconv.FormatInt(propertyID, 10))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	var out []models.Photo
	for _, u := range urls {
		ext, err := m.fetchOne(ctx, u, dir, len(out), referer)
		if err != nil {
			continue // skip broken image, keep going
		}
		name := fmt.Sprintf("%02d%s", len(out), ext)
		out = append(out, models.Photo{
			PropertyID: propertyID,
			Ordinal:    len(out),
			SourceURL:  u,
			LocalPath:  filepath.ToSlash(filepath.Join(strconv.FormatInt(propertyID, 10), name)),
		})
		// Be gentle with the image CDN.
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	return out, nil
}

func (m *Manager) fetchOne(ctx context.Context, url, dir string, idx int, referer string) (ext string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("http %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes))
	if err != nil {
		return "", err
	}
	// Confirm it's really an image: trust an image/* header, otherwise sniff the
	// bytes (CDNs like Azure Blob serve images as application/octet-stream).
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "image/") {
		ct = http.DetectContentType(data)
	}
	if !strings.HasPrefix(ct, "image/") {
		return "", fmt.Errorf("not an image (%s)", ct)
	}
	ext = extFor(ct, url)

	name := fmt.Sprintf("%02d%s", idx, ext)
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		return "", err
	}
	return ext, nil
}

func extFor(contentType, url string) string {
	switch {
	case strings.Contains(contentType, "jpeg"):
		return ".jpg"
	case strings.Contains(contentType, "png"):
		return ".png"
	case strings.Contains(contentType, "webp"):
		return ".webp"
	case strings.Contains(contentType, "avif"):
		return ".avif"
	case strings.Contains(contentType, "gif"):
		return ".gif"
	}
	// Fall back to the URL's extension if it looks sane.
	if i := strings.LastIndexByte(url, '.'); i >= 0 {
		e := strings.ToLower(url[i:])
		if j := strings.IndexAny(e, "?#"); j >= 0 {
			e = e[:j]
		}
		if len(e) <= 5 {
			return e
		}
	}
	return ".jpg"
}
