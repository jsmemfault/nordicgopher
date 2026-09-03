// Package artifactory ingests the public file server at files.nordicsemi.com.
//
// That host is a JFrog Artifactory instance and, unlike the rest of the
// nordicsemi.com estate, it is not behind the Cloudflare challenge. What it
// does restrict is metadata: an anonymous caller can list the repositories
// and each repository root, but api/storage below the root returns 404 and
// the deep-listing API returns 403. Downloads by known path are open.
//
// So the download tree is curated (see data/artifacts.json) rather than
// crawled, and every catalogue entry is verified at ingest time so a path
// that moves upstream shows up as a warning instead of a broken menu item.
package artifactory

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"nordicgopher/internal/gopher"
	"nordicgopher/internal/httpcache"
	"nordicgopher/internal/text"
	"nordicgopher/internal/tree"
)

// Base is the Artifactory root. Download selectors are formed by appending a
// catalogue path to it.
const Base = "https://files.nordicsemi.com/artifactory/"

// Config selects what to mirror.
type Config struct {
	CatalogPath string // path to artifacts.json; empty disables the download menu
	Verify      bool   // HEAD each catalogue entry before listing it
}

// Ingester writes the /files subtree.
type Ingester struct {
	HTTP *httpcache.Client
	Cfg  Config
	Log  *slog.Logger
}

type repository struct {
	Key         string `json:"key"`
	Description string `json:"description"`
	Type        string `json:"type"`
	URL         string `json:"url"`
	PackageType string `json:"packageType"`
}

type catalog struct {
	Groups []struct {
		Title string `json:"title"`
		Notes string `json:"notes"`
		Items []struct {
			Name string `json:"name"`
			Path string `json:"path"`
		} `json:"items"`
	} `json:"groups"`
}

func (in *Ingester) log() *slog.Logger {
	if in.Log != nil {
		return in.Log
	}
	return slog.Default()
}

// Run writes the subtree and returns a menu fragment for the site root.
func (in *Ingester) Run(t *tree.Tree, now time.Time) (gopher.Menu, error) {
	m := tree.Header("Nordic public file server",
		"files.nordicsemi.com holds the publicly downloadable SDKs, tools and "+
			"firmware images. Downloads below are streamed through this Gopher "+
			"server as binary items.")

	downloads := 0
	if in.Cfg.CatalogPath != "" {
		n, err := in.writeDownloads(t, now)
		if err != nil {
			in.log().Error("download catalogue failed", "err", err)
		} else {
			downloads = n
			m.Add(gopher.Link(gopher.TypeMenu,
				fmt.Sprintf("Downloads (%d verified artifacts)", n), "/files/downloads/"))
			m.Add(gopher.Blank())
		}
	}

	repos, err := in.repositories()
	if err != nil {
		in.log().Error("listing repositories failed", "err", err)
	} else {
		if err := in.writeRepositories(t, repos, now); err != nil {
			return nil, err
		}
		m.Add(gopher.Link(gopher.TypeMenu,
			fmt.Sprintf("Repository index (%d public repositories)", len(repos)),
			"/files/repositories/"))
	}

	m.Add(tree.Footer(now)...)
	if err := t.WriteMenu("/files", m); err != nil {
		return nil, err
	}

	in.log().Info("artifactory ingest complete", "repos", len(repos), "downloads", downloads)
	var frag gopher.Menu
	frag.Add(gopher.Link(gopher.TypeMenu,
		fmt.Sprintf("Files   - %d downloadable artifacts, %d repositories", downloads, len(repos)),
		"/files/"))
	return frag, nil
}

func (in *Ingester) repositories() ([]repository, error) {
	var repos []repository
	err := in.HTTP.GetJSON(Base+"api/repositories", nil, &repos)
	return repos, err
}

func (in *Ingester) writeRepositories(t *tree.Tree, repos []repository, now time.Time) error {
	m := tree.Header("Artifactory repository index",
		"Anonymous access can enumerate repositories but not their contents: "+
			"listing below a repository root requires authentication upstream. "+
			"Known download paths are served from the Downloads menu instead.")

	for _, r := range repos {
		line := fmt.Sprintf("  %-28s %-8s %s", r.Key, r.PackageType, r.Description)
		for _, l := range text.Wrap(line, gopher.MenuWidth, "    ") {
			m.Add(gopher.Info(l))
		}
	}
	m.Add(gopher.Blank())
	m.Add(gopher.URL("Browse the file server on the web", Base))
	m.Add(gopher.Link(gopher.TypeMenu, "Back to files", "/files/"))
	m.Add(tree.Footer(now)...)
	return t.WriteMenu("/files/repositories", m)
}

func (in *Ingester) writeDownloads(t *tree.Tree, now time.Time) (int, error) {
	raw, err := os.ReadFile(in.Cfg.CatalogPath)
	if err != nil {
		return 0, err
	}
	var cat catalog
	if err := json.Unmarshal(raw, &cat); err != nil {
		return 0, fmt.Errorf("parsing %s: %w", in.Cfg.CatalogPath, err)
	}

	m := tree.Header("Downloads",
		"Artifacts are streamed from files.nordicsemi.com through this server "+
			"as Gopher binary items, since Gopher clients cannot follow an "+
			"https link. Sizes are as reported upstream at generation time.")

	count := 0
	for _, g := range cat.Groups {
		m.Add(gopher.Info(g.Title))
		m.Add(gopher.Info(text.Rule("-", min(len(g.Title), text.Width))))
		if g.Notes != "" {
			for _, l := range text.Wrap(g.Notes, gopher.MenuWidth, "") {
				m.Add(gopher.Info(l))
			}
		}
		m.Add(gopher.Blank())

		for _, it := range g.Items {
			path := strings.TrimPrefix(it.Path, "/")
			size := ""
			if in.Cfg.Verify {
				n, err := in.head(Base + path)
				if err != nil {
					in.log().Warn("catalogue entry unavailable, omitting",
						"name", it.Name, "path", path, "err", err)
					continue
				}
				if n > 0 {
					size = humanBytes(n)
				}
			}
			label := it.Name
			if size != "" {
				label = fmt.Sprintf("%-44s %10s", text.Truncate(it.Name, 44), size)
			}
			m.Add(gopher.Link(gopher.TypeBinary, label, "/dl/"+path))
			count++
		}
		m.Add(gopher.Blank())
	}

	m.Add(gopher.Link(gopher.TypeMenu, "Back to files", "/files/"))
	m.Add(tree.Footer(now)...)
	return count, t.WriteMenu("/files/downloads", m)
}

// head checks that a catalogue path still resolves and returns its size when
// upstream reports one. Artifactory omits Content-Length on some HEAD
// responses, so a zero size is not an error.
func (in *Ingester) head(url string) (int64, error) {
	req, err := http.NewRequest("HEAD", url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", in.HTTP.UserAgent)
	resp, err := in.HTTP.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HEAD %s: %s", url, resp.Status)
	}
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		if n, err := strconv.ParseInt(cl, 10, 64); err == nil {
			return n, nil
		}
	}
	return 0, nil
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KiB", "MiB", "GiB"}
	f := float64(n)
	for _, u := range units {
		f /= unit
		if f < unit {
			return fmt.Sprintf("%.1f %s", f, u)
		}
	}
	return fmt.Sprintf("%.1f TiB", f/unit)
}
