package scraper

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"time"

	"github.com/chromedp/chromedp"
)

// BrowserFetcher renders pages in a headless Chrome/Chromium instance, so it
// can handle JS-rendered listings and many anti-bot challenge pages that defeat
// a plain HTTP client (VivaReal/ZAP, etc.). It is heavier than Fetcher — only
// sites flagged "render with browser" use it.
//
// It honours the same per-host politeness limiter as the HTTP fetcher.
type BrowserFetcher struct {
	execPath string
	lim      *limiter
	timeout  time.Duration
	// settleDelay is extra wait after load to let challenge JS / hydration run.
	settleDelay time.Duration
}

// NewBrowserFetcher builds a fetcher driving the Chrome/Chromium binary at
// execPath. If execPath is empty, chromedp tries to auto-detect one.
func NewBrowserFetcher(execPath string) *BrowserFetcher {
	return &BrowserFetcher{
		execPath:    execPath,
		lim:         newLimiter(5 * time.Second),
		timeout:     75 * time.Second,
		settleDelay: 4 * time.Second,
	}
}

func (b *BrowserFetcher) SetMinInterval(d time.Duration) { b.lim.SetMinInterval(d) }

// Available reports whether a usable Chrome/Chromium binary was found.
func (b *BrowserFetcher) Available() bool { return b.execPath != "" }

func (b *BrowserFetcher) Get(ctx context.Context, rawURL string) (string, error) {
	if b.execPath == "" {
		return "", fmt.Errorf("no Chrome/Chromium binary found; install chromium to use the headless-browser fetch mode")
	}
	if err := b.lim.wait(ctx, hostOf(rawURL)); err != nil {
		return "", err
	}

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(b.execPath),
		chromedp.Flag("headless", "new"),
		chromedp.UserAgent("Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"),
		chromedp.Flag("lang", "pt-BR"),
		chromedp.WindowSize(1366, 900),
		// Reduce the most obvious "I'm automated" signals.
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("disable-features", "site-per-process,Translate"),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("no-default-browser-check", true),
		// Required when running as root or in many containers/sandboxes.
		chromedp.NoSandbox,
	)

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	defer cancelAlloc()
	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)
	defer cancelBrowser()
	runCtx, cancelRun := context.WithTimeout(browserCtx, b.timeout)
	defer cancelRun()

	var html string
	start := time.Now()
	err := chromedp.Run(runCtx,
		chromedp.Navigate(rawURL),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Sleep(b.settleDelay), // let anti-bot JS resolve / content hydrate
		chromedp.OuterHTML("html", &html, chromedp.ByQuery),
	)
	slog.Debug("headless render", "url", rawURL, "bytes", len(html), "took", time.Since(start).Round(time.Millisecond), "err", err)
	if err != nil {
		return "", fmt.Errorf("headless render: %w", err)
	}
	return html, nil
}

// FindChrome locates a Chrome/Chromium binary, returning "" if none is found.
func FindChrome() string {
	for _, name := range []string{
		"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome",
	} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}
