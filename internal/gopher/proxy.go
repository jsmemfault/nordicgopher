package gopher

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Proxy streams an upstream file to a Gopher client as a binary item.
//
// Gopher clients cannot follow an https link, so a menu of downloads is
// useless unless the server fetches the bytes itself. The allowlist is the
// whole security model here: without it this handler is an open proxy, so it
// matches on the exact URL prefix rather than on the hostname alone.
type Proxy struct {
	// Allow maps a selector prefix to the upstream URL prefix it may reach.
	// A request's remaining path is appended to the upstream prefix, and
	// nothing outside that prefix is reachable.
	Allow map[string]string

	// MaxBytes caps a single transfer; zero means unlimited.
	MaxBytes int64

	// MaxConcurrent bounds transfers in flight. On a public server every
	// proxied selector is an outbound fetch a stranger can trigger, so this
	// is what keeps the host from being used to hammer the upstream. Zero
	// means DefaultMaxConcurrent.
	MaxConcurrent int

	HTTP      *http.Client
	UserAgent string
	Log       *slog.Logger

	sem  chan struct{}
	once sync.Once
}

// DefaultMaxConcurrent is the in-flight transfer limit when none is set.
const DefaultMaxConcurrent = 2

func (p *Proxy) logger() *slog.Logger {
	if p.Log != nil {
		return p.Log
	}
	return slog.Default()
}

func (p *Proxy) client() *http.Client {
	if p.HTTP != nil {
		return p.HTTP
	}
	return &http.Client{Timeout: 30 * time.Minute}
}

// Handle serves a proxied download. It writes raw bytes: a binary item is not
// dot-terminated and must not have its line endings rewritten.
func (p *Proxy) Handle(w io.Writer, selector, _ string) error {
	target, err := p.resolve(selector)
	if err != nil {
		WriteError(w, err.Error())
		return err
	}

	p.once.Do(func() {
		n := p.MaxConcurrent
		if n <= 0 {
			n = DefaultMaxConcurrent
		}
		p.sem = make(chan struct{}, n)
	})
	select {
	case p.sem <- struct{}{}:
		defer func() { <-p.sem }()
	default:
		WriteError(w, "too many downloads in progress, please try again")
		p.logger().Warn("proxied download refused: at capacity",
			"selector", selector, "max_concurrent", cap(p.sem))
		return nil
	}

	req, err := http.NewRequest("GET", target, nil)
	if err != nil {
		WriteError(w, "bad upstream request")
		return err
	}
	req.Header.Set("User-Agent", p.UserAgent)

	resp, err := p.client().Do(req)
	if err != nil {
		WriteError(w, "upstream unreachable")
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		WriteError(w, "upstream returned "+resp.Status)
		return fmt.Errorf("GET %s: %s", target, resp.Status)
	}

	var src io.Reader = resp.Body
	if p.MaxBytes > 0 {
		src = io.LimitReader(resp.Body, p.MaxBytes)
	}
	n, err := io.Copy(w, src)
	p.logger().Info("proxied download", "selector", selector, "upstream", target, "bytes", n)
	return err
}

// resolve maps a selector to an allowlisted upstream URL.
func (p *Proxy) resolve(selector string) (string, error) {
	for prefix, upstream := range p.Allow {
		if !strings.HasPrefix(selector, prefix) {
			continue
		}
		rest := strings.TrimPrefix(selector, prefix)
		// Path traversal, encoded traversal, and absolute or scheme-relative
		// paths are all attempts to leave the allowlisted prefix.
		if rest == "" || strings.Contains(rest, "..") ||
			strings.HasPrefix(rest, "/") || strings.Contains(rest, "//") {
			return "", fmt.Errorf("illegal download path")
		}
		unescaped, err := url.PathUnescape(rest)
		if err != nil || strings.Contains(unescaped, "..") {
			return "", fmt.Errorf("illegal download path")
		}
		target := upstream + unescaped
		// Re-parse and re-compare: this catches anything that survived the
		// checks above and still resolves outside the allowed prefix.
		u, err := url.Parse(target)
		if err != nil || !strings.HasPrefix(u.String(), upstream) {
			return "", fmt.Errorf("illegal download path")
		}
		return u.String(), nil
	}
	return "", fmt.Errorf("no download mapping for selector")
}
