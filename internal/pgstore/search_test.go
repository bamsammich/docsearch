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
	s.seedFor(builtinUser, id, title, chunks)
}

// seedFor writes one ready document holding chunks chunks into user's library.
func (s *SearchSuite) seedFor(user, id, title string, chunks int) {
	s.writeAs(user, `INSERT INTO documents
		   (user_id,doc_id,title,format,source_path,sha256,status,chunk_count)
		 VALUES ($1,$2,$3,'pdf','/x',$4,'ready',$5)`,
		user, id, title, id, chunks)
	for i := range chunks {
		kind, text := chunkBody(id, i)
		s.writeAs(user, `INSERT INTO chunks
			   (user_id,doc_id,ordinal,section,heading_path,text,kind,image_count)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,0)`,
			user, id, i, "1."+strconv.Itoa(i),
			title+" > Section "+strconv.Itoa(i), text, kind)
	}
}

// write runs one statement as the owner, in a transaction that named the
// built-in user.
func (s *SearchSuite) write(statement string, args ...any) {
	s.writeAs(builtinUser, statement, args...)
}

// writeAs runs one statement as the owner, in a transaction that named the
// user the row belongs to.
func (s *SearchSuite) writeAs(user, statement string, args ...any) {
	pgtest.WriteAs(s.T(), s.pg.Owner, user, func(tx *sql.Tx) {
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
		return "keyword-reference", "planet orbit planet orbit " + strconv.Itoa(i)
	}
	return "prose", "This section explains how to chart the planet and " +
		"assign an orbit number to a tracked moon in the star map, " +
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
	res := s.search(SearchParams{Query: "planet orbit", K: 6})
	s.Require().GreaterOrEqual(len(res), 2)
	s.NotEqual(res[0].DocID, res[1].DocID,
		"both documents are represented when results merge by within-document rank")
}

// An unscoped search takes each document's results from one walk of the
// library rather than a query per document, and must rank each as a search of
// that document alone does, adjustments included.
//
// The scores are compared, not the chunks: these chunks tie, and pg_textsearch
// orders a tie by how far its top-k search was asked to look.
func (s *SearchSuite) TestUnscopedSearchRanksEachDocumentAsItsOwnSearchDoes() {
	s.write(`INSERT INTO index_terms (user_id, doc_id, term, section)
		VALUES ($1, 'big', 'Planet, charting a', '1.9')`, builtinUser)

	for _, include := range []bool{false, true} {
		across := s.search(
			SearchParams{Query: "planet orbit", K: 25, IncludeKeywordReference: include},
		)
		for _, doc := range []string{"big", "small"} {
			got := scoresOf(across, doc)
			own := scoresOf(s.search(SearchParams{
				Query: "planet orbit", DocID: doc, K: 25, IncludeKeywordReference: include,
			}), doc)
			s.Require().NotEmpty(got, "%s is represented", doc)
			s.Require().GreaterOrEqual(len(own), len(got))
			s.Equal(own[:len(got)], got, "%s, keyword reference included: %v", doc, include)
		}
	}
}

// scoresOf is one document's adjusted scores, in result order.
func scoresOf(results []SearchResult, docID string) []float64 {
	var out []float64
	for _, r := range results {
		if r.DocID == docID {
			out = append(out, r.bm25)
		}
	}
	return out
}

// A document still being ingested has chunks but is not searchable yet.
func (s *SearchSuite) TestUnscopedSearchLeavesOutADocumentNotReady() {
	s.write(`INSERT INTO documents
		   (user_id,doc_id,title,format,source_path,sha256,status,chunk_count)
		 VALUES ($1,'half','Half Built','pdf','/x','half','indexing',1)`, builtinUser)
	s.write(`INSERT INTO chunks
		   (user_id,doc_id,ordinal,section,heading_path,text,kind,image_count)
		 VALUES ($1,'half',0,'1','Half Built > Planet','planet orbit planet orbit','prose',0)`, builtinUser)

	for _, r := range s.search(SearchParams{Query: "planet orbit", K: 25}) {
		s.NotEqual("half", r.DocID)
	}
}

// Scoped behaviour must be untouched by the cross-document merge.
func (s *SearchSuite) TestScopedSearchReturnsOnlyThatDocumentAndIsRanked() {
	res := s.search(SearchParams{Query: "planet orbit", DocID: "big", K: 5})
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
	res := s.search(SearchParams{Query: "planet orbit", DocID: "big", K: 5})
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
	def := s.search(SearchParams{Query: "planet orbit", DocID: "big", K: 25})
	first := bestKeywordRank(def)
	s.Require().NotZero(first, "keyword-reference chunks must remain reachable")
	s.Greater(first, 5, "the first keyword-reference result is pushed down")

	inc := s.search(SearchParams{
		Query: "planet orbit", DocID: "big", K: 25, IncludeKeywordReference: true,
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
			 VALUES ($1,'wild',$2,$3,$4,'calibrate the sensor before use','prose',0)`,
			builtinUser, i, strconv.Itoa(i+1), heading)
	}

	res := s.search(SearchParams{
		Query: "calibrate sensor", DocID: "wild", SectionFilter: "Wild > 10_ Rules", K: 10,
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
		VALUES ($1, 'big', 'Planet, charting a', '1.9')`, builtinUser)

	scoped := s.search(SearchParams{Query: "planet", DocID: "big", K: 5})
	s.Equal("1.9", firstOf(scoped, "big").Section, "scoped")

	across := s.search(SearchParams{Query: "planet", K: 6})
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

// A second account's library is scored with its own statistics.
//
// pg_textsearch 1.4.0 scores through the parent index with the first
// partition's statistics, which are the built-in library's. The second
// account here holds the rows the built-in library held before a third
// document changed its statistics, so its searches must score as the
// built-in library's did then. A term only the second library holds scored
// zero through the parent index, and was never found.
//
// The hyphen in the account is one create_account allows, and Postgres
// writes it into the name of the index it makes.
func (s *SearchSuite) TestASecondAccountSearchesItsOwnLibrary() {
	searches := []SearchParams{
		{Query: "planet orbit", K: 25},
		{Query: "planet orbit", DocID: "big", K: 25},
		{Query: "planet orbit", K: 8, SectionFilter: "Small Guide > Section 2"},
	}
	want := make([][]placement, len(searches))
	for i, p := range searches {
		want[i] = placements(s.search(p))
		s.Require().NotEmpty(want[i], "the built-in library matches %+v", p)
	}
	s.seed("noise", "Planet Primer", 40)

	const other = "other-team"
	_, err := s.pg.App.ExecContext(s.T().Context(), `SELECT create_account($1, $2, $3)`,
		other, "https://issuer.example", other)
	s.Require().NoError(err)
	s.seedFor(other, "big", "Big Manual", 24)
	s.seedFor(other, "small", "Small Guide", 6)
	theirs := New(s.pg.App, other)

	for i, p := range searches {
		got, err := theirs.Search(s.T().Context(), p)
		s.Require().NoError(err)
		s.Equal(want[i], placements(got), "%+v", p)
	}

	s.writeAs(other, `INSERT INTO chunks
		   (user_id,doc_id,ordinal,section,heading_path,text,kind,image_count)
		 VALUES ($1,'small',6,'1.6','Small Guide > Zebras','count the zebra stripes','prose',0)`,
		other)
	got, err := theirs.Search(s.T().Context(), SearchParams{Query: "zebra", K: 8})
	s.Require().NoError(err)
	s.Require().Len(got, 1, "a term the built-in library lacks")
	s.Equal("Small Guide > Zebras", got[0].HeadingPath)
}

// placement is where a result landed and what it scored, without the chunk
// identifier, which differs between two libraries holding the same rows.
type placement struct {
	DocID   string
	Section string
	Rank    int
	BM25    float64
}

func placements(results []SearchResult) []placement {
	out := make([]placement, 0, len(results))
	for _, r := range results {
		out = append(out, placement{DocID: r.DocID, Section: r.Section, Rank: r.Rank, BM25: r.bm25})
	}
	return out
}

// An account whose partition is gone holds no chunks, and its search finds
// nothing rather than failing.
func (s *SearchSuite) TestAnAccountWithNoPartitionFindsNothing() {
	res, err := New(s.pg.App, "nobody").Search(
		s.T().Context(), SearchParams{Query: "planet orbit", K: 8},
	)
	s.Require().NoError(err)
	s.Empty(res)
}
