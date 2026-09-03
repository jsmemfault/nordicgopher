// Package httpcache is a small conditional-request cache for ingesters.
//
// The mirror is rebuilt on a schedule, and most upstream content does not
// change between runs. Storing the ETag and revalidating turns a nightly
// rebuild into a few hundred 304s, which keeps the job fast and keeps us
// inside GitHub's rate limit without needing a token for modest trees.
package httpcache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Client fetches URLs, revalidating against an on-disk cache. It is safe for
// concurrent use: cache entries are keyed by URL hash so writers never
// collide, the throttle is serialised, and the counters are atomic.
type Client struct {
	Dir       string        // cache directory
	UserAgent string        // sent on every request; identifies the mirror
	MinGap    time.Duration // minimum interval between requests to be polite
	HTTP      *http.Client
	Log       *slog.Logger

	// Retries is the number of times a throttled or failed request is
	// retried with backoff. A cold cache on a datacenter address gets
	// throttled where a warm cache on a home connection never does, so
	// giving up on the first 429 makes the first run the one most likely to
	// fail. Zero means DefaultRetries.
	Retries int

	mu   sync.Mutex
	last time.Time

	hits, misses, revalidated atomic.Int64
}

type meta struct {
	URL          string    `json:"url"`
	ETag         string    `json:"etag,omitempty"`
	LastModified string    `json:"last_modified,omitempty"`
	Fetched      time.Time `json:"fetched"`
	Status       int       `json:"status"`
}

// DefaultRetries is the retry count when none is configured.
const DefaultRetries = 4

// noRetry marks a failure that retrying cannot fix.
const noRetry = -1 * time.Nanosecond

func New(dir string) *Client {
	return &Client{
		Dir:       dir,
		UserAgent: "nordicgopher/0.2 (gopher mirror; contact the repository owner)",
		MinGap:    150 * time.Millisecond,
		HTTP:      &http.Client{Timeout: 60 * time.Second},
		Retries:   DefaultRetries,
	}
}

func (c *Client) logger() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.Default()
}

func (c *Client) key(url string) string {
	sum := sha256.Sum256([]byte(url))
	return filepath.Join(c.Dir, hex.EncodeToString(sum[:])[:32])
}

func (c *Client) throttle() {
	if c.MinGap <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if gap := time.Since(c.last); gap < c.MinGap {
		time.Sleep(c.MinGap - gap)
	}
	c.last = time.Now()
}

// Get returns the body for url, using a conditional request when a cached
// copy exists, retrying with backoff if the request is throttled.
func (c *Client) Get(url string, hdr http.Header) ([]byte, error) {
	tries := c.Retries
	if tries <= 0 {
		tries = DefaultRetries
	}

	var lastErr error
	for attempt := 0; attempt < tries; attempt++ {
		body, retryAfter, err := c.get(url, hdr)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if retryAfter < 0 {
			// Not a throttle or a transient fault; retrying will not help.
			return nil, err
		}
		if attempt == tries-1 {
			break
		}
		// Exponential backoff, unless the server said how long to wait.
		wait := time.Duration(1<<attempt) * time.Second
		if retryAfter > 0 {
			wait = retryAfter
		}
		c.logger().Warn("request throttled, backing off",
			"url", url, "attempt", attempt+1, "of", tries, "wait", wait, "err", err)
		time.Sleep(wait)
	}
	return nil, lastErr
}

// get performs one attempt. The returned duration is the delay the server
// asked for, zero to back off on our own schedule, or negative when the
// failure is not worth retrying.
func (c *Client) get(url string, hdr http.Header) ([]byte, time.Duration, error) {
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return nil, noRetry, err
	}
	base := c.key(url)

	var m meta
	cached := false
	if b, err := os.ReadFile(base + ".meta"); err == nil {
		if json.Unmarshal(b, &m) == nil {
			cached = true
		}
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, noRetry, err
	}
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("User-Agent", c.UserAgent)
	if cached {
		if m.ETag != "" {
			req.Header.Set("If-None-Match", m.ETag)
		}
		if m.LastModified != "" {
			req.Header.Set("If-Modified-Since", m.LastModified)
		}
	}

	c.throttle()
	resp, err := c.HTTP.Do(req)
	if err != nil {
		// A cached body is better than a failed rebuild.
		if cached {
			if b, rerr := os.ReadFile(base + ".body"); rerr == nil {
				c.logger().Warn("upstream unreachable, serving cached copy", "url", url, "err", err)
				c.hits.Add(1)
				return b, 0, nil
			}
		}
		return nil, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified && cached {
		c.revalidated.Add(1)
		b, err := os.ReadFile(base + ".body")
		return b, noRetry, err
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		// Rate limiting and upstream faults are transient. A mirror would
		// rather republish its last good copy than fail the run, and being
		// rate limited is the moment that matters most: the cached body is
		// there precisely because the resource was fetched before.
		if cached && retryable(resp.StatusCode) {
			if b, rerr := os.ReadFile(base + ".body"); rerr == nil {
				c.logger().Warn("upstream returned a transient error, serving cached copy",
					"url", url, "status", resp.Status)
				c.hits.Add(1)
				return b, noRetry, nil
			}
		}
		err := fmt.Errorf("GET %s: %s: %s", url, resp.Status, snippet(body))
		if !retryable(resp.StatusCode) {
			return nil, noRetry, err
		}
		return nil, retryDelay(resp), err
	}

	c.misses.Add(1)
	os.WriteFile(base+".body", body, 0o644)
	nm, _ := json.Marshal(meta{
		URL:          url,
		ETag:         resp.Header.Get("ETag"),
		LastModified: resp.Header.Get("Last-Modified"),
		Fetched:      time.Now(),
		Status:       resp.StatusCode,
	})
	os.WriteFile(base+".meta", nm, 0o644)
	return body, noRetry, nil
}

// retryDelay reads how long the server asked us to wait. GitHub sends
// Retry-After when it throttles, and x-ratelimit-reset when the hourly
// budget is spent; the latter can be an hour away, which is longer than an
// ingest should ever sit waiting, so it is reported as "back off normally"
// and left to the caller's patience.
func retryDelay(resp *http.Response) time.Duration {
	if v := resp.Header.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
			if d := time.Duration(secs) * time.Second; d <= 2*time.Minute {
				return d
			}
		}
	}
	return 0
}

// GetJSON fetches and decodes a JSON document.
func (c *Client) GetJSON(url string, hdr http.Header, v any) error {
	b, err := c.Get(url, hdr)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// Stats renders cache counters for the ingest log.
func (c *Client) Stats() string {
	return fmt.Sprintf("fetched=%d revalidated=%d stale-served=%d",
		c.misses.Load(), c.revalidated.Load(), c.hits.Load())
}

// retryable reports whether a status is worth falling back to cache for:
// rate limiting, request timeouts, and server-side faults.
func retryable(status int) bool {
	switch status {
	case http.StatusForbidden, http.StatusTooManyRequests, http.StatusRequestTimeout:
		return true
	}
	return status >= 500
}

func snippet(b []byte) string {
	if len(b) > 200 {
		b = b[:200]
	}
	return string(b)
}
