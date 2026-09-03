package text

import "net/url"

// Options controls a conversion.
type Options struct {
	// Width is the column at which text is wrapped.
	Width int
	// BaseURL resolves relative links. Mirrored documents routinely link to
	// sibling files ("see MIGRATION.md"), which is meaningless once the
	// document is a footnote list in gopherspace, so relative targets are
	// made absolute against the document's upstream location.
	BaseURL string
}

func (o Options) width() int {
	if o.Width > 0 {
		return o.Width
	}
	return Width
}

// resolve makes target absolute against BaseURL when it is relative.
func (o Options) resolve(target string) string {
	if o.BaseURL == "" {
		return target
	}
	u, err := url.Parse(target)
	if err != nil || u.IsAbs() || target == "" {
		return target
	}
	// A bare fragment refers inside the same document; there is nowhere for
	// it to point once the document is plain text.
	if u.Path == "" && u.Fragment != "" {
		return target
	}
	base, err := url.Parse(o.BaseURL)
	if err != nil {
		return target
	}
	return base.ResolveReference(u).String()
}
