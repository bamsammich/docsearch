# Embedding rerank probe — negative result

Date: 2026-08-02 · Model: `sentence-transformers/all-MiniLM-L6-v2`
Method: top-50 BM25 candidates per query (emitted from the production
`store.Search` path), reranked by cosine similarity on `heading_path + text`.
Throwaway script, no schema change, no production dependency.

## Verdict: do not build the pipeline

Measured twice, at two success criteria. The second is the one that counts.

| success criterion | rescued | displaced | net |
|---|---|---|---|
| correct chunk into **top-3** | 6 | 3 | **+3** |
| correct chunk into **top-8** | 2 | 4 | **−2** |

top-8 is the right target: the consumer is a model reading k=8 full chunks, not
a person scanning three results. **At that k reranking is actively harmful**,
and conceptual-slang — the category it existed for — nets −1.

The first measurement used the wrong criterion, not the wrong candidate pool;
both runs reranked the same 50-deep BM25 pool. Recording both so the correction
is visible rather than silently replaced.

**At top-3, the category the probe existed to fix nets exactly zero.**

| category | n | rescued | displaced | net |
|---|---|---|---|---|
| conceptual-slang | 20 | 2 | 2 | **0** |
| heading-term | 16 | 1 | 0 | +1 |
| body-term | 5 | 1 | 0 | +1 |
| figure-dominated | 6 | 1 | 0 | +1 |
| spans-boundary | 6 | 1 | 1 | 0 |

Rescued: q05, q16, q23, q36, q39, q50. Displaced: q17, q35, q43.

## Two findings that matter more than the totals

**1. The console terms of art carry actively wrong priors, as suspected.**
Every query built on vocabulary that means something else in general web text
got substantially *worse*:

| query | BM25 rank | reranked |
|---|---|---|
| submaster **fader** to hold a **look** | 4 | **12** |
| record a snapshot of the current **look** | 13 | **33** |
| **cue stack** on a **fader** | 6 | **22** |

A general-purpose sentence embedder reads "look", "fader" and "cue stack" as
everyday English. BM25's ignorance is neutral; the embedder's confidence is
wrong. This is the failure mode worth naming, and it lands precisely on the
operator-slang queries a reranker was supposed to rescue.

**2. Reranking undoes the structural fix.** q17 ("step by step assign a fader
to control a group master") was the query that motivated the keyword-reference
work. The `kind` filter moved it to rank 1. Reranking pushes it back to rank 9.
A cheap, explainable, structural change beat the dense model on the very case
that prompted the investigation.

## Where a reranker did help

The rescues cluster on queries where the answer is real prose that BM25 ranked
4th–9th — q23 (5→1), q39 (9→1), q50 (5→1). That is reordering within an
already-good candidate set, not recall. It is the weakest possible case for
adding an embedding pipeline, because the answer was already retrievable and a
caller reading five results would have found it.

## Decision

Question closed, alongside the index-term boost finding. Both were plausible
retrieval improvements; both measured out as noise on this corpus. Phase 6 is
**not** justified on this evidence.

Reopen only if the corpus changes character — many small documents rather than
two large manuals — or if a domain-adapted embedding model becomes available.
If reopened, the shape stays as scoped: sqlite-vec as a **reranker over the
top ~50 BM25 hits**, never as primary retrieval, and the displacement count
stays a release gate.

## Related negative result

The back-of-book index-term boost fires but is too diffuse to discriminate: on
conceptual queries it matched 25, 46 and 21 sections respectively. A boost
applied to 46 of 827 sections is a constant, not a signal. It earns its place
on precise terms and does nothing on the queries it was hoped would benefit.
