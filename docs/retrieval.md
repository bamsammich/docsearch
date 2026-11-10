# Retrieval quality

The measured figures behind the README, how they were produced, and the retrieval ideas that were measured and rejected. Back to the [README](../README.md). The full write-ups are in [docs/research/](research/).

## Where the measured figures come from

Every number on this page was measured on one corpus against one committed
query set. They describe BM25 over that corpus; they are not properties of the
software and not a forecast for your documents.

| | |
|---|---|
| reference corpus | two technical manuals — one paginated and section-numbered (944 chunks), one non-paginated and unnumbered (265) |
| query set | `tests/retrieval/queries.json` — 54 committed, 53 scored |
| baselines | `tests/retrieval/baseline-{A,B,C}*.txt` |
| full detail | [docs/research/retrieval-quality.md](research/retrieval-quality.md) |

This checkout holds neither `tests/retrieval/queries.json` nor the baseline files; `tests/retrieval/` holds only `sites.json`, the documentation-site query set behind the site figures in the README ([site-retrieval-baseline.md](research/site-retrieval-baseline.md)).

A corpus with different vocabulary, structure or size will produce different
numbers. What transfers between corpora is the mechanism behind each result,
not the figure — so each result below states its mechanism first.

## Use the tools in the right order

Cold keyword search is the weakest way to use the index, and every figure here
is measured that way because it is the hardest framing.

**Orient first.** `list_documents` → `outline` → `search` with `section_filter`
set to the chapter that plainly covers the question. Of the 15 queries that
single-shot search missed at k=8, **14 came back at rank 1** once scoped.

**That 14/15 is an upper bound, not a cold-start figure.** The harness chose
each `section_filter` using the query's expected section — an oracle it had and
a real caller does not. A model picking the chapter from the outline alone will
sometimes pick wrong, and the true figure is lower by however often that
happens. It has not been measured.

The conclusion survives the caveat comfortably: those 15 queries score **0/15**
single-shot, so even a substantially degraded real-world recovery rate is a
large gain. What is established is that the answers are present and reachable
once the search is scoped; what is not established is how reliably a model
scopes it correctly.

The mechanism generalizes even where the number does not: scoping a lexical
search to a section shrinks the candidate pool to one where the document's own
vocabulary dominates. That is why `outline`'s tool description says to call it
before searching.

## Measured and rejected

Two plausible retrieval improvements were built, measured, and rejected. Both
are recorded here rather than only in `docs/research/` because these are the
decisions most likely to be re-litigated by someone who has not seen the
numbers.

### Back-of-book index-term boost — too diffuse to discriminate

Boosting chunks whose section a matching back-of-book index term names sounds
free, and on precise lookups it works. It fails on exactly the conceptual
queries it was meant to rescue, because a back-of-book term routinely names
dozens of sections at once.

On the reference corpus the index parsed cleanly — 1,841 references, 100%
resolving to known sections — and the conceptual queries under test matched
**25, 46 and 21 sections** respectively. A boost applied to 46 of 827 sections
is a constant, not a signal. It is still in the code because it costs nothing
and helps precise lookups, but it does not do the job it was added for.

### Dense reranking — actively harmful at the k that matters

`all-MiniLM-L6-v2` over the top 50 BM25 candidates. Scored into top-3 it was
net +3 across 53 queries. Scored into **top-8 — the k a model actually reads —
it is net −2** (2 rescued, 4 displaced), and the conceptual-slang category it
existed to fix nets −1.

**A specialist register defeats a general-purpose embedder.** The model's
priors come from general English, so a corpus whose terms of art collide with
ordinary words gets ranked confidently wrong — and any corpus with a specialist
register (legal, medical, industrial, in-house jargon) should expect the same.
On the reference corpus the collisions are lighting-console terms: "fader",
"look", "cue stack", "executor" and "programmer" all mean something else in
general English, and every query built on them got *worse*:

| query | BM25 rank | reranked |
|---|---|---|
| sub**master** **fader** to hold a **look** | 4 | **12** |
| record a snapshot of the current **look** | 13 | **33** |
| **cue stack** on a **fader** | 6 | **22** |

BM25's ignorance is neutral. The embedder's confidence is wrong, and it is
wrong precisely on the vernacular a semantic model was supposed to help with.

**And it undoes a cheaper fix.** The query that motivated classifying
keyword-reference chunks — q17, "step by step assign a fader to control a group
master" — moved to **rank 1** on that structural change. Reranking pushes it
back to **rank 9**. A cheap, explainable, structural change beat the dense
model on the very case that prompted the investigation.

Verdict: no vector index, no embedding step in ingest, no second recall path.
Reopen only if the corpus changes character or a domain-adapted model appears;
if reopened, the shape stays sqlite-vec as a reranker over the top ~50 BM25
hits, never primary retrieval, with displacement count as a release gate.

## Evaluating an index

```bash
go run ./cmd/docsearch-eval --dsn "$DOCSEARCH_DSN"             # retrieval evaluation
go run ./cmd/docsearch-eval --dsn DSN --self-label             # measure any corpus, no query set
```

`--self-label` builds each query from a chunk's own heading and body and asks
whether that chunk comes back, so it runs on any index the moment it is
ingested. Its limits are severe and it prints them: a self-labelled query
shares terms with its answer by construction, so it cannot see the vocabulary
gap, and a document cut into fixed windows scores 100% because every slice
still holds distinctive words. It catches the failures that leave an index
where nothing is findable — collapsed structure, boilerplate dominating the
term mass, chunk sizes in the wrong unit. Use `verify` to judge structure.

`tests/retrieval/queries.json` is a committed query set with expected sections
recorded before the queries were ever run. **A query that misses stays in the
file unedited** — rewording queries until they pass overfits to a moving test
set. Baselines are committed alongside it so the effect of a change is
measurable.

**recall@8 is the headline metric**, not top-3. The consumer is a model reading
k=8 full chunks, not a person scanning three results.

Cold single-shot search on the reference corpus, by query cohort:

| cohort | @1 | @3 | **@8** | @20 |
|---|---|---|---|---|
| all (n=53) | 42% | 53% | **68%** | 79% |
| heading-term (16) | 62% | 88% | **94%** | 94% |
| body-term (5) | 60% | 60% | **80%** | 80% |
| spans-boundary (6) | 50% | 67% | **83%** | 100% |
| conceptual-slang (20) | 20% | 25% | **45%** | 65% |
| figure-dominated (6) | 33% | 33% | **50%** | 67% |

Add orientation (`outline` → `section_filter`) and 14 of the 15 remaining @8
misses come back at rank 1 — an **upper bound under oracle section selection**,
see [Use the tools in the right order](#use-the-tools-in-the-right-order).

Of the 11 queries missing even at @20, 9 share at least one term with their
target section and 2 share none — so most are reachable in principle and the
failure is ranking depth, not absence.
