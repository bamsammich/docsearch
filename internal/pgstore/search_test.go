package pgstore

import (
	"database/sql"
	"strconv"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/pgtest"
	"github.com/bamsammich/docsearch/internal/schema"
)

// builtinUser owns every row these cases write.
const builtinUser = "default"

// SearchSuite searches a two-document index where one document is
// deliberately much smaller, reproducing the lopsided shape that makes
// cross-document BM25 scores incomparable.
type SearchSuite struct {
	suite.Suite
	pg *pgtest.DB
	st *Store
}

func TestSearch(t *testing.T) { suite.Run(t, new(SearchSuite)) }

func (s *SearchSuite) SetupTest() {
	s.pg = pgtest.Start(s.T())
	// The migrations, the same ones a deployment runs and sqlc types its
	// queries against.
	s.Require().NoError(schema.Create(s.T().Context(), s.pg.Owner))
	s.seed("big", "Big Manual", 24)
	s.seed("small", "Small Guide", 6)
	// The store reads as the restricted role a request uses, not as the
	// owner that seeded the rows.
	s.st = New(s.pg.App, builtinUser)
}

// seed writes one ready document holding chunks chunks.
func (s *SearchSuite) seed(id, title string, chunks int) {
	s.write(`INSERT INTO documents
		   (user_id,doc_id,title,format,source_path,sha256,status,chunk_count)
		 VALUES ($1,$2,$3,'pdf','/x',$4,'ready',$5)`,
		builtinUser, id, title, id, chunks)
	for i := range chunks {
		kind, text := chunkBody(id, i)
		s.write(`INSERT INTO chunks
			   (user_id,doc_id,ordinal,section,heading_path,text,kind,image_count)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,0)`,
			builtinUser, id, i, "1."+strconv.Itoa(i),
			title+" > Section "+strconv.Itoa(i), text, kind)
	}
}

// write runs one statement as the owner, in a transaction that named the user
// the row belongs to.
func (s *SearchSuite) write(statement string, args ...any) {
	pgtest.WriteAs(s.T(), s.pg.Owner, builtinUser, func(tx *sql.Tx) {
		_, err := tx.ExecContext(s.T().Context(), statement, args...)
		s.Require().NoError(err)
	})
}

// chunkBody is one chunk's kind and text.
//
// Keyword-reference entries are modelled as they actually are: short and
// term-dense, so BM25 favours them on incidental token overlap. Without a
// penalty they would take the top slots.
func chunkBody(id string, i int) (string, string) {
	if id == "big" && i >= 14 {
		return "keyword-reference", "universe dmx universe dmx " + strconv.Itoa(i)
	}
	return "prose", "This section explains how to configure the universe and " +
		"assign a dmx address to a patched fixture in the show file, " +
		"with worked examples and surrounding narrative " + strconv.Itoa(i)
}

func (s *SearchSuite) search(p SearchParams) []SearchResult {
	res, err := s.st.Search(s.T().Context(), p)
	s.Require().NoError(err)
	return res
}

// An unscoped search must give every document a shot at the top slots rather
// than letting the smaller corpus sweep them on incomparable IDF.
func (s *SearchSuite) TestUnscopedSearchDoesNotLetOneDocumentSweepTheTop() {
	res := s.search(SearchParams{Query: "universe dmx", K: 6})
	s.Require().GreaterOrEqual(len(res), 2)
	s.NotEqual(res[0].DocID, res[1].DocID,
		"both documents are represented when results merge by within-document rank")
}

// Scoped behaviour must be untouched by the cross-document merge.
func (s *SearchSuite) TestScopedSearchReturnsOnlyThatDocumentAndIsRanked() {
	res := s.search(SearchParams{Query: "universe dmx", DocID: "big", K: 5})
	s.Require().NotEmpty(res)
	for i, r := range res {
		s.Equal("big", r.DocID)
		s.Equal(i+1, r.Rank, "ranks run consecutively from one")
		if i > 0 {
			s.GreaterOrEqual(r.bm25, res[i-1].bm25, "best first at position %d", i)
		}
	}
}

func (s *SearchSuite) TestRelevanceIsPositiveHigherIsBetterAndOrderPreserving() {
	res := s.search(SearchParams{Query: "universe dmx", DocID: "big", K: 5})
	for i, r := range res {
		s.GreaterOrEqual(r.Relevance, 0.0)
		s.LessOrEqual(r.Relevance, 1.0)
		if i > 0 {
			s.LessOrEqual(r.Relevance, res[i-1].Relevance, "order-preserving at %d", i)
		}
	}
}

// Keyword-reference entries are deprioritised, never dropped: a keyword lookup
// is a legitimate query and they are its correct answers.
func (s *SearchSuite) TestKeywordReferenceIsDeprioritisedButReachable() {
	def := s.search(SearchParams{Query: "universe dmx", DocID: "big", K: 25})
	first := bestKeywordRank(def)
	s.Require().NotZero(first, "keyword-reference chunks must remain reachable")
	s.Greater(first, 5, "the first keyword-reference result is pushed down")

	inc := s.search(SearchParams{
		Query: "universe dmx", DocID: "big", K: 25, IncludeKeywordReference: true,
	})
	s.Less(bestKeywordRank(inc), first, "include_keyword_reference lifts them")
}

// bestKeywordRank is where the best keyword-reference entry landed, or zero
// when none came back at all.
func bestKeywordRank(res []SearchResult) int {
	for _, r := range res {
		if r.Kind == "keyword-reference" {
			return r.Rank
		}
	}
	return 0
}

// A section filter is matched as written. Under LIKE, the underscore in
// "10_ Rules" would be a wildcard and sweep in "100 Rules".
func (s *SearchSuite) TestASectionFilterHasNoWildcards() {
	s.write(`INSERT INTO documents
		   (user_id,doc_id,title,format,source_path,sha256,status,chunk_count)
		 VALUES ($1,'wild','Wild','pdf','/x','wild','ready',3)`, builtinUser)
	for i, heading := range []string{
		"Wild > 10_ Rules", "Wild > 10_ Rules > Detail", "Wild > 100 Rules > Detail",
	} {
		s.write(`INSERT INTO chunks
			   (user_id,doc_id,ordinal,section,heading_path,text,kind,image_count)
			 VALUES ($1,'wild',$2,$3,$4,'calibrate the fixture before use','prose',0)`,
			builtinUser, i, strconv.Itoa(i+1), heading)
	}

	res := s.search(SearchParams{
		Query: "calibrate fixture", DocID: "wild", SectionFilter: "Wild > 10_ Rules", K: 10,
	})
	s.Len(res, 2, "the section and its child")
	for _, r := range res {
		s.NotContains(r.HeadingPath, "100 Rules", "a section the filter does not name")
	}
}

// A chunk whose section a matching back-of-book entry names is lifted, both
// in a search of its document and in a search of the whole library, which
// reads every document's entries in one query.
func (s *SearchSuite) TestAnIndexEntryLiftsTheSectionItNames() {
	s.write(`INSERT INTO index_terms (user_id, doc_id, term, section)
		VALUES ($1, 'big', 'Universe, patching a', '1.9')`, builtinUser)

	scoped := s.search(SearchParams{Query: "universe", DocID: "big", K: 5})
	s.Equal("1.9", firstOf(scoped, "big").Section, "scoped")

	across := s.search(SearchParams{Query: "universe", K: 6})
	s.Equal("1.9", firstOf(across, "big").Section, "across the library")
}

// firstOf is the best-ranked result from one document.
func firstOf(results []SearchResult, docID string) SearchResult {
	for _, r := range results {
		if r.DocID == docID {
			return r
		}
	}
	return SearchResult{}
}
