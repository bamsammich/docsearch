package discover

import (
	"context"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/bamsammich/docsearch/internal/pystr"
)

// Signature is what a site's not-found page looks like: the words its
// template holds, counted.
type Signature struct {
	counts map[string]int
	total  int
}

// Found reports whether the site answers 200 for a page that does not exist.
func (s *Signature) Found() bool { return s != nil && s.total > 0 }

// NotFoundSignature is what this site's not-found page looks like, when it
// answers 200 for one.
//
// Two improbable paths are probed rather than one. A site that soft-404s
// returns near-identical bodies for both, and what they share is the
// template; what differs is the path echoed back. One probe could not tell a
// template from a real page.
//
// A site that answers an honest status gets no signature, which is most of
// them and needs no further checking.
func NotFoundSignature(
	ctx context.Context,
	f Fetcher,
	origin string,
	revalidate bool,
) (*Signature, error) {
	var probes [2]*Signature
	for i, name := range probePaths {
		res, err := f.Fetch(ctx, origin+"/"+name, revalidate)
		if err != nil || res.Status != 200 {
			return nil, nil //nolint:nilnil // no signature is the ordinary answer, not a failure
		}
		text, err := PageText(res.Body)
		if err != nil {
			return nil, err
		}
		probes[i] = tokens(text)
	}
	if resembles(probes[0], probes[1]) < probeAgreement {
		// Two different bodies for two absent pages: this site serves
		// something real, or something random. Either way there is no
		// template to match against.
		return nil, nil //nolint:nilnil // see above
	}
	shared := intersect(probes[0], probes[1])
	if shared.total < minSignatureTokens {
		// Too little to tell a template from a short page that happens to
		// share a few common words.
		return nil, nil //nolint:nilnil // see above
	}
	return shared, nil
}

// LooksAbsent reports whether a 200 response is really the site's not-found
// page. A site answering 200 for everything would otherwise feed its error
// page to the index, and the completeness gate would score that a success.
func LooksAbsent(body []byte, signature *Signature) (bool, error) {
	if !signature.Found() {
		return false, nil
	}
	text, err := PageText(body)
	if err != nil {
		return false, err
	}
	return covers(signature, tokens(text)) >= resemblance, nil
}

// PageText is a page's visible text, which is what the probes compare.
func PageText(body []byte) (string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	doc.Find("script, style").Remove()
	pageBody := doc.Find("body").First()
	if pageBody.Length() == 0 {
		return strings.Join(strings.Fields(doc.Text()), " "), nil
	}
	return strings.Join(strings.Fields(pageBody.Text()), " "), nil
}

// tokens counts a text's words, lowercased.
func tokens(text string) *Signature {
	s := &Signature{counts: map[string]int{}}
	for _, m := range word.FindAllString(text, -1) {
		s.counts[pystr.Lower(m)]++
		s.total++
	}
	return s
}

// intersect keeps the lower count of each word the two share.
func intersect(a, b *Signature) *Signature {
	out := &Signature{counts: map[string]int{}}
	for w, n := range a.counts {
		if m := min(n, b.counts[w]); m > 0 {
			out.counts[w] = m
			out.total += m
		}
	}
	return out
}

// resembles is symmetric token overlap, 0 to 1, for comparing two probes.
func resembles(a, b *Signature) float64 {
	if a.total == 0 || b.total == 0 {
		return 0
	}
	return float64(intersect(a, b).total) / float64(max(a.total, b.total))
}

// covers is how much of a signature appears in a page, 0 to 1.
//
// Containment rather than similarity, because a not-found page is the
// template plus whatever it echoes back about the request. Comparing the two
// symmetrically scores a longer page lower for saying more, which is
// backwards: the question is whether the template is present.
func covers(signature, page *Signature) float64 {
	if signature.total == 0 || page.total == 0 {
		return 0
	}
	return float64(intersect(signature, page).total) / float64(signature.total)
}
