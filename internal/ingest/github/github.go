// Package github ingests the public Nordic GitHub organisations.
//
// GitHub is the mirror's most productive source: the API is open, needs no
// authentication for modest trees, and the content (READMEs, release notes)
// is already prose. It is also where the nRF Connect SDK documentation lives
// in reStructuredText form, so the conversion path exercised here is the same
// one the documentation ingest will use.
package github

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"nordicgopher/internal/gopher"
	"nordicgopher/internal/httpcache"
	"nordicgopher/internal/text"
	"nordicgopher/internal/tree"
)

// Config selects what to mirror.
type Config struct {
	// Root is the selector prefix for this subtree, e.g. "/github". It is
	// not always "/github": when the tree is served as a subdirectory of an
	// existing gopherhole, every selector has to carry that prefix.
	Root        string
	Orgs        []string
	MaxRepos    int    // per organisation, most recently pushed first
	MaxReleases int    // release notes per repository
	Token       string // optional; raises the rate limit from 60/h to 5000/h
}

// Ingester writes the /github subtree.
type Ingester struct {
	HTTP *httpcache.Client
	Cfg  Config
	Log  *slog.Logger
}

const apiBase = "https://api.github.com"

type repo struct {
	Name          string    `json:"name"`
	FullName      string    `json:"full_name"`
	Description   string    `json:"description"`
	HTMLURL       string    `json:"html_url"`
	Homepage      string    `json:"homepage"`
	DefaultBranch string    `json:"default_branch"`
	Language      string    `json:"language"`
	Topics        []string  `json:"topics"`
	Stars         int       `json:"stargazers_count"`
	Forks         int       `json:"forks_count"`
	OpenIssues    int       `json:"open_issues_count"`
	Archived      bool      `json:"archived"`
	Fork          bool      `json:"fork"`
	PushedAt      time.Time `json:"pushed_at"`
	License       struct {
		SPDX string `json:"spdx_id"`
	} `json:"license"`
}

type readme struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Content     string `json:"content"`
	Encoding    string `json:"encoding"`
	HTMLURL     string `json:"html_url"`
	DownloadURL string `json:"download_url"`
}

type release struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	HTMLURL     string    `json:"html_url"`
	PublishedAt time.Time `json:"published_at"`
	Prerelease  bool      `json:"prerelease"`
	Draft       bool      `json:"draft"`
	Assets      []struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

func (in *Ingester) log() *slog.Logger {
	if in.Log != nil {
		return in.Log
	}
	return slog.Default()
}

func (in *Ingester) headers() http.Header {
	h := http.Header{}
	h.Set("Accept", "application/vnd.github+json")
	h.Set("X-GitHub-Api-Version", "2022-11-28")
	if in.Cfg.Token != "" {
		h.Set("Authorization", "Bearer "+in.Cfg.Token)
	}
	return h
}

// requestsPerRepo is what mirroring one repository costs: its README and its
// releases.
const requestsPerRepo = 2

// reservedRequests is held back for the other ingesters -- notably the
// documentation ingest, which needs exactly one request for the git tree and
// should not be starved by this one.
const reservedRequests = 4

// Run writes the subtree and returns a menu fragment for the site root.
func (in *Ingester) Run(t *tree.Tree, now time.Time) (gopher.Menu, error) {
	// Size the run to the budget actually available rather than assuming
	// one. Unauthenticated GitHub allows 60 requests an hour and each
	// repository costs two, so the configured default overspends the budget
	// on a cold cache -- and every failed attempt spends more of it, which
	// is how a first deployment ends up unable to run even the one-request
	// documentation ingest. Asking for the remaining budget is free: the
	// rate_limit endpoint does not count against it.
	if budget, ok := in.budget(); ok {
		affordable := affordableRepos(budget, len(in.Cfg.Orgs))
		switch {
		case affordable <= 0:
			return nil, fmt.Errorf(
				"GitHub API budget exhausted (%d requests left this hour); "+
					"set GITHUB_TOKEN to raise the limit from 60 to 5000/hour, "+
					"or wait for the window to reset", budget)
		case in.Cfg.MaxRepos == 0 || in.Cfg.MaxRepos > affordable:
			in.log().Warn("limiting repositories to fit the remaining API budget",
				"requested", repoLimitLabel(in.Cfg.MaxRepos), "using", affordable,
				"budget", budget,
				"hint", "set GITHUB_TOKEN to raise the limit from 60 to 5000/hour")
			in.Cfg.MaxRepos = affordable
		}
	}

	root := in.Cfg.Root
	orgMenu := tree.Header("Nordic Semiconductor on GitHub",
		"Public repositories, README files and release notes, converted to plain text.")

	var mirrored int
	for _, org := range in.Cfg.Orgs {
		repos, err := in.repos(org)
		if err != nil {
			in.log().Error("listing repositories failed", "org", org, "err", err)
			continue
		}
		n, err := in.writeOrg(t, org, repos, now)
		if err != nil {
			in.log().Error("writing org subtree failed", "org", org, "err", err)
			continue
		}
		mirrored += n
		orgMenu.Add(gopher.Link(gopher.TypeMenu,
			fmt.Sprintf("%-24s %3d repositories", org, n),
			root+"/"+strings.ToLower(org)+"/"))
	}
	orgMenu.Add(tree.Footer(now)...)
	if err := t.WriteMenu(root, orgMenu); err != nil {
		return nil, err
	}

	in.log().Info("github ingest complete", "repos", mirrored, "cache", in.HTTP.Stats())
	var frag gopher.Menu
	frag.Add(gopher.Link(gopher.TypeMenu,
		fmt.Sprintf("GitHub  - %d public repositories, READMEs and release notes", mirrored),
		root+"/"))
	return frag, nil
}

// budget returns the remaining API requests this hour. It reports false when
// the limit cannot be read, in which case the configured limits are used as
// given: guessing a budget would be worse than trusting the operator.
func (in *Ingester) budget() (int, bool) {
	// A token raises the limit to 5000/hour, which no configuration here can
	// exhaust, so there is nothing to size against.
	if in.Cfg.Token != "" {
		return 0, false
	}
	in.log().Warn("no GitHub token: the unauthenticated API allows 60 requests/hour",
		"hint", "set GITHUB_TOKEN to mirror every repository in one run")

	var rl struct {
		Resources struct {
			Core struct {
				Remaining int   `json:"remaining"`
				Reset     int64 `json:"reset"`
			} `json:"core"`
		} `json:"resources"`
	}
	if err := in.HTTP.GetJSON(apiBase+"/rate_limit", in.headers(), &rl); err != nil {
		in.log().Warn("could not read the API rate limit; proceeding with configured limits", "err", err)
		return 0, false
	}
	core := rl.Resources.Core
	in.log().Info("github API budget",
		"remaining", core.Remaining,
		"resets_in", time.Until(time.Unix(core.Reset, 0)).Round(time.Minute))
	return core.Remaining, true
}

// affordableRepos is how many repositories per organisation fit in a budget
// of API requests: one listing page per organisation, two requests per
// repository, and a reserve left for the other ingesters.
func affordableRepos(budget, orgs int) int {
	if orgs < 1 {
		orgs = 1
	}
	spendable := budget - reservedRequests - orgs
	if spendable < 0 {
		return 0
	}
	return spendable / requestsPerRepo / orgs
}

func repoLimitLabel(n int) string {
	if n == 0 {
		return "all"
	}
	return fmt.Sprint(n)
}

func (in *Ingester) repos(org string) ([]repo, error) {
	var all []repo
	for page := 1; page <= 5; page++ {
		var batch []repo
		url := fmt.Sprintf("%s/orgs/%s/repos?per_page=100&sort=pushed&direction=desc&page=%d",
			apiBase, org, page)
		if err := in.HTTP.GetJSON(url, in.headers(), &batch); err != nil {
			if page == 1 {
				return nil, err
			}
			break
		}
		all = append(all, batch...)
		if len(batch) < 100 {
			break
		}
	}
	// Most recently pushed first, so a capped run mirrors the live work.
	sort.SliceStable(all, func(i, j int) bool { return all[i].PushedAt.After(all[j].PushedAt) })
	if in.Cfg.MaxRepos > 0 && len(all) > in.Cfg.MaxRepos {
		all = all[:in.Cfg.MaxRepos]
	}
	return all, nil
}

func (in *Ingester) writeOrg(t *tree.Tree, org string, repos []repo, now time.Time) (int, error) {
	orgSel := in.Cfg.Root + "/" + strings.ToLower(org)
	m := tree.Header(org+" repositories",
		fmt.Sprintf("%d repositories, most recently updated first.", len(repos)))

	written := 0
	for _, r := range repos {
		sel := orgSel + "/" + slug(r.Name)
		if err := in.writeRepo(t, sel, r, now); err != nil {
			in.log().Warn("skipping repository", "repo", r.FullName, "err", err)
			continue
		}
		written++

		label := r.Name
		if r.Archived {
			label += " (archived)"
		}
		m.Add(gopher.Link(gopher.TypeMenu, text.Truncate(label, gopher.MenuWidth), sel+"/"))
		if r.Description != "" {
			for _, l := range text.Wrap("      "+r.Description, gopher.MenuWidth, "") {
				m.Add(gopher.Info(l))
			}
		}
		m.Add(gopher.Blank())
	}
	m.Add(tree.Footer(now)...)
	return written, t.WriteMenu(orgSel, m)
}

func (in *Ingester) writeRepo(t *tree.Tree, sel string, r repo, now time.Time) error {
	m := tree.Header(r.FullName, r.Description)

	facts := [][2]string{
		{"Language", r.Language},
		{"License", r.License.SPDX},
		{"Default branch", r.DefaultBranch},
		{"Stars", fmt.Sprint(r.Stars)},
		{"Forks", fmt.Sprint(r.Forks)},
		{"Open issues", fmt.Sprint(r.OpenIssues)},
		{"Last push", r.PushedAt.UTC().Format("2006-01-02")},
	}
	if len(r.Topics) > 0 {
		facts = append(facts, [2]string{"Topics", strings.Join(r.Topics, ", ")})
	}
	if r.Archived {
		facts = append(facts, [2]string{"Status", "ARCHIVED (read-only upstream)"})
	}
	for _, f := range facts {
		if f[1] == "" || f[1] == "0" {
			continue
		}
		m.Add(gopher.Info(fmt.Sprintf("  %-15s %s", f[0]+":", f[1])))
	}
	m.Add(gopher.Blank())

	// README. The endpoint returns whatever file the repository actually has,
	// which for the SDK repositories is reStructuredText rather than Markdown.
	if rm, err := in.readme(r); err != nil {
		in.log().Debug("no readme", "repo", r.FullName, "err", err)
	} else {
		body, derr := decode(rm)
		if derr != nil {
			return derr
		}
		base := rm.HTMLURL
		if base == "" {
			base = r.HTMLURL + "/blob/" + r.DefaultBranch + "/" + rm.Path
		}
		converted := text.AutoOpts(rm.Name, body, text.Options{
			Width:   text.Width,
			BaseURL: base,
		})
		page := tree.Page(r.FullName+" - "+rm.Name, converted, base, now)
		if err := t.WriteFile(sel+"/readme.txt", page); err != nil {
			return err
		}
		m.Add(gopher.Link(gopher.TypeText, "README ("+rm.Name+")", sel+"/readme.txt"))
	}

	// Release notes.
	if rels, err := in.releases(r); err != nil {
		in.log().Debug("no releases", "repo", r.FullName, "err", err)
	} else if len(rels) > 0 {
		if err := in.writeReleases(t, sel, r, rels, now); err != nil {
			return err
		}
		m.Add(gopher.Link(gopher.TypeMenu,
			fmt.Sprintf("Releases (%d most recent)", len(rels)), sel+"/releases/"))
	}

	m.Add(gopher.Blank())
	m.Add(gopher.URL("Canonical repository on the web", r.HTMLURL))
	if r.Homepage != "" {
		m.Add(gopher.URL("Project homepage", r.Homepage))
	}
	m.Add(tree.Footer(now)...)
	return t.WriteMenu(sel, m)
}

func (in *Ingester) writeReleases(t *tree.Tree, sel string, r repo, rels []release, now time.Time) error {
	m := tree.Header(r.Name+" releases", "Release notes, newest first.")
	for _, rel := range rels {
		tag := rel.TagName
		title := rel.Name
		if title == "" {
			title = tag
		}
		label := fmt.Sprintf("%-24s %s", text.Truncate(tag, 24), rel.PublishedAt.UTC().Format("2006-01-02"))
		if rel.Prerelease {
			label += "  (pre-release)"
		}

		body := strings.TrimSpace(rel.Body)
		if body == "" {
			body = "(No release notes were published for this tag.)"
		} else {
			body = text.MarkdownOpts(body, text.Options{Width: text.Width, BaseURL: rel.HTMLURL})
		}
		if len(rel.Assets) > 0 {
			var b strings.Builder
			b.WriteString(body)
			b.WriteString("\n\nAssets\n------\n")
			for _, a := range rel.Assets {
				b.WriteString(fmt.Sprintf("  %-46s %8s\n", text.Truncate(a.Name, 46), humanBytes(a.Size)))
				b.WriteString("    " + a.URL + "\n")
			}
			body = b.String()
		}

		file := sel + "/releases/" + slug(tag) + ".txt"
		if err := t.WriteFile(file, tree.Page(r.FullName+" "+title, body, rel.HTMLURL, now)); err != nil {
			return err
		}
		m.Add(gopher.Link(gopher.TypeText, label, file))
	}
	m.Add(gopher.Blank())
	m.Add(gopher.Link(gopher.TypeMenu, "Back to "+r.Name, sel+"/"))
	m.Add(tree.Footer(now)...)
	return t.WriteMenu(sel+"/releases", m)
}

func (in *Ingester) readme(r repo) (readme, error) {
	var rm readme
	err := in.HTTP.GetJSON(apiBase+"/repos/"+r.FullName+"/readme", in.headers(), &rm)
	return rm, err
}

func (in *Ingester) releases(r repo) ([]release, error) {
	n := in.Cfg.MaxReleases
	if n <= 0 {
		n = 10
	}
	var rels []release
	url := fmt.Sprintf("%s/repos/%s/releases?per_page=%d", apiBase, r.FullName, n)
	if err := in.HTTP.GetJSON(url, in.headers(), &rels); err != nil {
		return nil, err
	}
	var out []release
	for _, rel := range rels {
		if rel.Draft {
			continue
		}
		out = append(out, rel)
	}
	return out, nil
}

func decode(rm readme) (string, error) {
	if rm.Encoding != "base64" {
		return rm.Content, nil
	}
	clean := strings.NewReplacer("\n", "", "\r", "", " ", "").Replace(rm.Content)
	b, err := base64.StdEncoding.DecodeString(clean)
	if err != nil {
		return "", fmt.Errorf("decoding %s: %w", rm.Path, err)
	}
	return string(b), nil
}

var reSlug = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// slug makes a name safe as a single path element. Selectors are transmitted
// verbatim over the wire, so keeping them to a conservative character set
// avoids quoting problems in clients.
func slug(s string) string {
	s = reSlug.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-.")
	if s == "" || s == "." || s == ".." {
		return "item"
	}
	return path.Clean(s)
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
