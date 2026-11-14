# Attribution

**Licence status: UNVERIFIED.** Details of what was found are in `SOURCES.md`; this file only says what is used and what is open.

| What | Source | Status |
|---|---|---|
| Product data (names, descriptions, prices, brands, stock, weights, source reviews) | DummyJSON, https://dummyjson.com (project: https://github.com/Ovi/DummyJSON, "DummyJSON - Muhammad Ovi (Owais)") | The code repository is MIT. No separate terms of use for the hosted data were found (retrieved 2026-10-27). |
| Product images (committed in the frontend repo, `static/img/products/`, converted to WebP) | `cdn.dummyjson.com/product-images/...` | **No licence or provenance found.** "Covered by MIT" is an assumption, not a fact. |
| Brand and product names (Rolex, Nike, Chanel, ...) | as in the source | Trademarks of their owners, demo data only. |
| `rating`, `review_count` | derived from the review-service seed (generated demo reviews), not from DummyJSON | Own data. |

The MIT licence requires its copyright notice and permission text to be kept with copies of the covered work. This repository does not yet ship that notice for the DummyJSON data.

## Check before this leaves the demo (public repo, public site, anything with real customers)

1. Ask the DummyJSON maintainer, or find a written statement, about the licence of the **images** and of the hosted dataset. Until then do not describe them as MIT-licensed.
2. If it cannot be confirmed: replace the images (own photos, or a source with a clear licence) and the brand names, or keep the shop private.
3. Add the MIT notice for DummyJSON (and the frontend's `NOTICE`) if the data stays; the frontend repo currently has no attribution file at all.
4. Re-check that no real person's data is in the source reviews (reviewer e-mails were dropped; the reviewer names are generated demo names, not verified).
