# Retrieval quality — findings and the closed questions

Date: 2026-08-02 · Corpus: grandMA2 manual (944 chunks) + QLC+ docs (265)
Query set: `tests/retrieval/queries.json`, 54 committed / 53 scored
Baselines: `tests/retrieval/baseline-{A,B,C}*.txt`

## Headline

`recall@8`, not top-3. The consumer is a model reading k=8 full chunks, not a
person scanning three results.

| cohort | @1 | @3 | **@8** | @20 |
|---|---|---|---|---|
| all (n=53) | 42% | 53% | **68%** | 79% |
| heading-term (16) | 62% | 88% | 94% | 94% |
| body-term (5) | 60% | 60% | 80% | 80% |
| spans-boundary (6) | 50% | 67% | 83% | 100% |
| conceptual-slang (20) | 20% | 25% | 45% | 65% |
| figure-dominated (6) | 33% | 33% | 50% | 67% |

The narrow @1–@3 gap suggested retrieval failure. It is neither retrieval nor
ranking-quality failure: correct chunks cluster at ranks 4–20. The problem is
ranking depth, and the fix turned out not to be a better ranker.

## The agentic loop, and what its number actually means

`outline` → `search` with `section_filter` recovers **14 of the 15** queries
that single-shot search missed at k=8, all at rank 1.

**This is an upper bound under oracle section selection.** The harness picked
each `section_filter` using the query's recorded expected section — knowledge
it had and a real caller does not. A model choosing the chapter from the
outline alone will sometimes choose wrong, and the true cold-start figure is
lower by however often that happens. That has not been measured, and 14/15
should never be quoted as a cold-start result.

Labelled the same way as the other soft numbers in this project: the partial
blindness of the query set, and q28's grading artifact.

What the measurement does establish:

- the answers are **present and reachable** once the search is scoped
- these 15 queries score **0/15** single-shot, so even a badly degraded real
  recovery rate is a large gain
- the remedy is **guidance, not machinery** — which is why no retrieval code
  was added and both tool descriptions now tell callers to orient first

What it does not establish: how reliably a model picks the right chapter.

## Closed questions

**Index-term boost — too diffuse.** 1,841 references parse at 100%, but on
conceptual queries the boost matched 25, 46 and 21 sections. A boost spanning
46 of 827 sections is a constant, not a signal. Retained because it costs
nothing and helps precise lookups; it does not do the job it was added for.

**Dense reranking — harmful at the k that matters.** See
[embedding-rerank-probe.md](embedding-rerank-probe.md). Net +3 scored into
top-3, **net −2 scored into top-8**. The corpus's terms of art ("fader",
"look", "cue stack") carry actively wrong priors in a general-purpose model,
and reranking reverses the keyword-reference fix on the very query that
motivated it. Closed.

**Keyword-reference classification — confirmed and shipped.** The one
hypothesis that survived measurement: +17pp on spans-boundary, +12pp top-1 on
heading-term, no regression anywhere.

## The irreducible case

**q34, "strike the lamps on my moving lights".** Zero term overlap with its
target. "Strike" is theatre vernacular meaning *switch on* (and, confusingly,
*dismantle* in a different context); the manual says "Lamp On". No lexical
method can bridge that, and the reranking probe did not either.

This is the honest floor for a pure-lexical index: vocabulary with no shared
surface form and no shared distributional context in general text.

If cases like this accumulate — track them via the query log below — the
proportionate remedy is a small **synonym/glossary table** mapping vernacular
to document vocabulary, applied at query expansion time. That is bounded,
inspectable and auditable, unlike an embedding pipeline, and it fails visibly
rather than silently. One case does not justify building it.

## Recommended next work: log real queries

Hand-authored queries were the only option before the system existed, and
their limits are documented (partial blindness, expectation contamination,
authorial guesses about what operators ask).

Six months of real usage is a better eval set than anything hand-written. Worth
recording per query:

- the query text, `doc_id`, and whether `section_filter` was used
- the ranks returned and which chunk (if any) the caller went on to expand
- whether the caller followed up with `get_context`, a scoped re-search, or
  simply stopped

The follow-up behaviour is the signal. A caller that immediately re-searches
with a filter reveals a first-attempt miss without anyone labelling anything,
and the ratio of scoped to unscoped first attempts measures whether the
orientation guidance in the tool descriptions is actually being followed.

It would also settle the open question above empirically: how often does a
model pick the right chapter from the outline?

And it would answer whether conceptual-slang weakness matters in practice. It
is the worst category by a wide margin (45% @8) and may simply not be how
callers phrase things once they have read an outline.

Structured request logging already exists in the server (method, tool, duration,
outcome; never the token, never full query text). Extending it to a queryable
log is a small change, gated on a decision about retaining query text.
