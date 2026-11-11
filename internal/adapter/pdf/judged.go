package pdf

import (
	"context"
	"strconv"

	"github.com/bamsammich/docsearch/internal/judge"
	"github.com/bamsammich/docsearch/internal/pystr"
)

// HeadingJudge says how likely each heading candidate is to be a section
// heading. *judge.Client is one.
type HeadingJudge interface {
	Headings(
		ctx context.Context,
		document string,
		candidates []judge.Heading,
	) (judge.Verdicts, error)
}

// Judged is the options that have an Extractor ask the judge at url, with
// token as its bearer token, and none when url is empty.
func Judged(url, token string) ([]Option, error) {
	if url == "" {
		return nil, nil
	}
	client, err := judge.New(url, token)
	if err != nil {
		return nil, err
	}
	return []Option{WithJudge(client)}, nil
}

// VetoBelow is the probability under which a judge's verdict takes a
// candidate out. Set low on purpose: a judge removes only what it is sure
// is not a heading, and a doubtful candidate stays with the rules.
const VetoBelow = 0.2

// candidate is a line the rules would take for a heading.
type candidate struct {
	text string
	at   pagePoint
}

// vetoer returns the candidates a judge ruled out. Nil rules out nothing,
// which is a build with no judge.
type vetoer func(candidates []candidate) map[pagePoint]bool

// judgeReport is what a build's judge did: who judged, what it was asked,
// what it took out, and why its answer was set aside when it was.
type judgeReport struct {
	judge  string
	unused string
	vetoed []string
	asked  int
}

// diagnostic is the report as the "heading_judge" diagnostic, which
// domain.NewStructureReport reads into the persisted structure report.
func (r *judgeReport) diagnostic() map[string]any {
	out := map[string]any{"asked": r.asked, "vetoed": r.vetoed}
	if r.judge != "" {
		out["judge"] = r.judge
	}
	if r.unused != "" {
		out["unused"] = r.unused
	}
	return out
}

// newVetoer asks j about candidates on the document's pages and records what
// happened under diagnostics["heading_judge"]. A build may ask more than
// once, numbered candidates and then titles, so the report accumulates.
func newVetoer(
	ctx context.Context,
	j HeadingJudge,
	name string,
	pages []Page,
	diagnostics map[string]any,
) vetoer {
	if j == nil {
		return nil
	}
	report := &judgeReport{vetoed: []string{}}
	return func(candidates []candidate) map[pagePoint]bool {
		if len(candidates) == 0 {
			return nil
		}
		vetoed := ask(ctx, j, name, pages, candidates, report)
		diagnostics["heading_judge"] = report.diagnostic()
		return vetoed
	}
}

// ask puts candidates to j and returns the ones it ruled out, adding what
// happened to report.
func ask(
	ctx context.Context,
	j HeadingJudge,
	name string,
	pages []Page,
	candidates []candidate,
	report *judgeReport,
) map[pagePoint]bool {
	report.asked += len(candidates)
	asked := make([]judge.Heading, len(candidates))
	for i, c := range candidates {
		asked[i] = question(pages, c)
	}
	verdicts, err := j.Headings(ctx, name, asked)
	if err != nil {
		// The rules' answer stands. An ingest never fails on advice.
		report.unused = "the judge could not be asked: " + err.Error()
		return nil
	}
	if verdicts.Judge != "" {
		report.judge = verdicts.Judge
	}
	return vetoes(candidates, asked, verdicts, report)
}

// vetoes is the candidates whose verdict falls under VetoBelow, unless that
// is every one of them. A judge that finds no heading at all in a document
// whose type sizes declare some is more likely broken than right, and taking
// out every heading would leave the document without the structure the
// grader requires.
func vetoes(
	candidates []candidate,
	asked []judge.Heading,
	verdicts judge.Verdicts,
	report *judgeReport,
) map[pagePoint]bool {
	out := map[pagePoint]bool{}
	var named []string
	for i, c := range candidates {
		if p, ok := verdicts.Heading[asked[i].ID]; ok && p < VetoBelow {
			out[c.at] = true
			named = append(named, asked[i].ID+" "+c.text)
		}
	}
	if len(out) == len(candidates) {
		report.unused = "the judge ruled out every candidate, which a document with " +
			"heading sizes cannot mean"
		return nil
	}
	report.vetoed = append(report.vetoed, named...)
	return out
}

// question is c as a judge is asked about it: an ID naming its page and
// line, and the nearest lines with text either side of it on the page.
func question(pages []Page, c candidate) judge.Heading {
	lines := pages[c.at.page].Lines
	q := judge.Heading{
		ID:   "p" + strconv.Itoa(c.at.page+1) + ":l" + strconv.Itoa(c.at.line),
		Text: c.text,
		Page: c.at.page + 1,
	}
	for i := c.at.line - 1; i >= 0 && q.Before == ""; i-- {
		q.Before = pystr.Strip(lines[i].Text)
	}
	for i := c.at.line + 1; i < len(lines) && q.After == ""; i++ {
		q.After = pystr.Strip(lines[i].Text)
	}
	return q
}

// numberedCandidates are the lines findBodyHeadings would consider as
// numbered headings, before its ordering rule: a heading-size line that is
// not furniture and carries a section number, on a page after the printed
// contents that is not a numbered list.
//
// A section the printed contents declare is left out. The author declared
// it, and taking it out would turn a judge's doubt into a contents entry
// missing from the body, which fails the ingest.
func (n numbered) numberedCandidates() []candidate {
	s := headingScanner{sizes: n.headingSizes, boiler: n.boiler}
	var out []candidate
	for pno, p := range n.pages {
		if pno > n.tocPageMax && !s.numberedList(p.Lines) {
			out = n.pageCandidates(out, s, pno, p.Lines)
		}
	}
	return out
}

// pageCandidates appends the numbered candidates among one page's lines.
func (n numbered) pageCandidates(
	out []candidate,
	s headingScanner,
	pno int,
	lines []Line,
) []candidate {
	for i, ln := range lines {
		if _, ok := s.numberedText(ln); !ok {
			continue
		}
		m := sectionLine.FindStringSubmatch(ln.Text)
		if _, declared := n.titles[m[1]]; declared {
			continue
		}
		title, _, _ := headingTitle(lines, i, m[2])
		out = append(out, candidate{
			text: pystr.Strip(m[1] + ". " + title),
			at:   pagePoint{page: pno, line: i},
		})
	}
	return out
}

// titleCandidates are the titles titlePlacements placed, as candidates.
func titleCandidates(placements []placement) []candidate {
	out := make([]candidate, len(placements))
	for i, p := range placements {
		out[i] = candidate{text: p.title, at: pagePoint{page: p.page, line: p.line}}
	}
	return out
}
