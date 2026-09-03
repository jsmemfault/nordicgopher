package text

import "net/url"

// Options controls a conversion.
//
// The resolution tables exist because a documentation set is not a collection
// of standalone files. Sphinx projects define substitutions, hyperlink
// targets and cross-reference labels centrally, and a document read without
// them is missing words, not just links: nRF Connect SDK prose says "The
// |NCS| is a modern, unified SDK", and dropping the substitution leaves "The
// is a modern, unified SDK". The tables are supplied by the ingester, which
// knows where a given project keeps them; the converter stays generic.
type Options struct {
	// Width is the column at which text is wrapped.
	Width int

	// BaseURL resolves relative links. Mirrored documents routinely link to
	// sibling files ("see MIGRATION.md"), which is meaningless once the
	// document is a footnote list in gopherspace, so relative targets are
	// made absolute against the document's upstream location.
	BaseURL string

	// Substitutions maps a substitution name without its pipes ("NCS") to
	// its replacement text, from reStructuredText "|name| replace::"
	// definitions.
	Substitutions map[string]string

	// LinkTargets maps a hyperlink target name to its URL, from named
	// reStructuredText targets (".. _`Zephyr`: https://..."). A reference
	// written `Zephyr`_ resolves through this table.
	LinkTargets map[string]string

	// RefTargets maps a Sphinx cross-reference label to where it lives in
	// the mirror. Values are used verbatim as footnote targets, so an
	// ingester can point them at mirror selectors rather than the web.
	RefTargets map[string]string

	// Include resolves a reStructuredText ".. include::" path to its
	// content. Returning false leaves the directive out of the output.
	// Paths are passed through exactly as written in the document.
	Include func(path string) (string, bool)

	// MaxIncludeDepth bounds include recursion. Zero means the default.
	MaxIncludeDepth int
}

func (o Options) width() int {
	if o.Width > 0 {
		return o.Width
	}
	return Width
}

func (o Options) maxIncludeDepth() int {
	if o.MaxIncludeDepth > 0 {
		return o.MaxIncludeDepth
	}
	return 6
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
