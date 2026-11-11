# Heading judge

docsearch finds a PDF's headings from type sizes and section numbers when the PDF has no outline. Those rules sometimes take a procedure step set at a heading size, such as `9. Tap Save`, for a section. The step then heads a section of its own, and its `9.` can make the ordering rule refuse the real sections 3 to 8 that follow it.

A heading judge is a service you run that docsearch asks about those candidates before it decides. docsearch defines the contract below and knows nothing about what answers it, so a judge can wrap any model or a hand-written rule. Without a judge URL, docsearch asks nothing and sends no document text anywhere. Back to the [README](../README.md).

## Turning the judge on

| | flag | environment | docsearch.yaml (server) |
|---|---|---|---|
| judge base URL | `--judge-url` | `DOCSEARCH_JUDGE_URL` | `ingest.judge_url` |
| bearer token sent to the judge | none, environment only | `DOCSEARCH_JUDGE_TOKEN` | none, a secret |

Set both on the worker, which runs queued ingests, and on the server, which reads PDFs for `docsearch inspect` and for a synchronous ingest. A document ingested where no judge is set is built by the rules alone.

The judge receives the text of heading candidates and the lines around them. Point the URL only at a service you trust with your documents.

## Documents and lines the judge sees

Only PDFs whose structure is inferred are judged. A PDF with an outline is not, because its bookmarks are the author's own declaration. Markdown, HTML and Word documents are not either, because their headings are markup.

Within a judged PDF, the candidates come from two places:

| the PDF has | candidates | left out |
|---|---|---|
| section numbers | every heading-size line carrying a number, before the ordering rule runs | lines on a page read as a numbered list, and sections the printed contents declare |
| no section numbers | every title the title rule placed | nothing |

A section the printed contents declare is never put to the judge. Taking one out would leave a contents entry with no heading in the body, and that disagreement fails the ingest.

## The request

docsearch sends `POST <base URL>/v1/headings` with a JSON body, and an `Authorization: Bearer` header when a token is set. A document with more than 200 candidates is sent in several requests.

```json
{
  "document": "console-manual.pdf",
  "candidates": [
    {
      "id": "p2:l2",
      "text": "9. Tap Save",
      "page": 2,
      "before": "Body text of topic 2.",
      "after": "The settings are kept."
    }
  ]
}
```

`id` names the candidate within the request. `page` is 1-based. `before` and `after` are the nearest lines with text on the same page, and are left out at a page's edge.

## The answer

The judge answers `200 OK` with the probability, from 0 to 1, that each candidate is a section heading:

```json
{
  "judge": "jev-bridge/1 jev-latest",
  "headings": [
    {"id": "p2:l2", "heading": 0.03}
  ]
}
```

`judge` names the service and whatever model it used, and is recorded with the document. A candidate the judge leaves out of `headings` gets no verdict. A verdict for an `id` that was not asked about, a verdict without `heading`, or a `heading` outside 0 to 1 breaks the contract, and docsearch then sets aside the whole answer.

Later contracts, such as one for tagging, will get their own path beside `/v1/headings` under the same base URL.

## How docsearch uses the answer

A judge can only take candidates out. It never adds a heading the rules did not find.

| the judge | docsearch |
|---|---|
| scores a candidate under 0.2 | reads the line as body text. A numbered line taken out no longer advances the numbering, so the sections after it are found |
| scores a candidate at 0.2 or above, or leaves it out | leaves the candidate to the rules |
| scores every candidate under 0.2 | ignores the answer, because a document whose type sizes declare headings has at least one |
| fails, times out after 60 seconds, or breaks the contract | builds the document by the rules alone. The ingest goes ahead |

## Seeing what the judge changed

A judged document's structure report, stored in `documents.warnings`, gains three fields:

| field | holds |
|---|---|
| `judge` | the name the judge's answer gave |
| `judged_not_headings` | each candidate it took out, as its `id` and text |
| `judge_unused` | why the rules decided alone: the judge failed, broke the contract, or ruled out every candidate |

Each one also becomes a warning on the document, so `list_documents`, `ingest_status` and `docsearch jobs` show what the judge took out, or why it changed nothing. A document built without a judge has none of the three fields.

Re-ingesting a document after you swap or remove the judge can give it different headings. The `judge` field tells you which judge built the version you have.

## Writing a judge

A judge is a small HTTP service. Each candidate maps naturally to a yes/no question for a model that answers with a probability: "Is this line a section heading in a manual, given the lines around it?" A judge built on TypeSafe's Jev, for example, sends the candidates as typed boolean questions and returns Jev's probabilities by `id`. The judge, not docsearch, holds the model's API key.

Keep the answer conservative. docsearch acts only on a score under 0.2, so a judge that is unsure should answer near 0.5 or leave the candidate out.
