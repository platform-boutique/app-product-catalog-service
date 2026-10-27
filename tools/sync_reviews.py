#!/usr/bin/env python3
"""Derive `rating` and `review_count` of every catalog product from the review-service seed.

Why: the review-service seed (apps/review-service/data/reviews.json) is the truth the shop UI shows
(product page, grid, review list). The catalog used to carry the DummyJSON average instead, so
"Top rated" sorted by one number while the page displayed another, and review_count was always 0.

Rule (identical to the review-service's summary, `round(avg(rating), 1)` in Postgres):
    rating       = mean of the review ratings, rounded to ONE decimal, half away from zero
                   (computed in exact integer arithmetic, no floating point)
    review_count = number of reviews for the product
A product without reviews gets rating 0 and review_count 0 (the same as "no summary" in review-service).

Standard library only. Deterministic and idempotent: running it twice changes nothing, and the file is
written in the same format as tools/fetch_dataset.py (indent 2, UTF-8, trailing newline).

    python3 tools/sync_reviews.py --reviews ../review-service/data/reviews.json          # rewrite products.json
    python3 tools/sync_reviews.py --reviews ../review-service/data/reviews.json --check  # exit 1 if it would change

Reviews that reference an unknown product id are an error (a typo would otherwise silently drop reviews).
Only `rating` and `review_count` are touched; the DummyJSON `reviews` array inside products.json is source
data that the service ignores and is left as it is.
"""
from __future__ import annotations

import argparse
import json
import sys
from collections import defaultdict
from pathlib import Path

DEFAULT_SEED_DIR = Path(__file__).resolve().parent.parent / "data" / "seed"


def round1(total: int, count: int) -> float:
    """total/count rounded to one decimal, half away from zero, exactly (total, count >= 0)."""
    if count == 0:
        return 0.0
    tenths = (20 * total + count) // (2 * count)  # floor(10*total/count + 1/2)
    return tenths / 10


def summaries(reviews: list[dict]) -> dict[str, tuple[float, int]]:
    sums: dict[str, list[int]] = defaultdict(lambda: [0, 0])
    for i, r in enumerate(reviews):
        pid, rating = r.get("product_id"), r.get("rating")
        if not isinstance(pid, str) or not pid or not isinstance(rating, int) or isinstance(rating, bool) \
                or not 1 <= rating <= 5:
            raise ValueError(f"review #{i}: bad product_id or rating: {r!r}")
        sums[pid][0] += rating
        sums[pid][1] += 1
    return {pid: (round1(t, n), n) for pid, (t, n) in sums.items()}


def sync(products: list[dict], reviews: list[dict]) -> int:
    """Update products in place; return the number of products whose values changed."""
    sm = summaries(reviews)
    unknown = sorted(set(sm) - {p["id"] for p in products})
    if unknown:
        raise ValueError(f"reviews for unknown product ids: {unknown}")
    changed = 0
    for p in products:
        rating, count = sm.get(p["id"], (0.0, 0))
        if p.get("rating") != rating or p.get("review_count") != count:
            changed += 1
        p["rating"], p["review_count"] = rating, count
    return changed


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--reviews", required=True, type=Path, help="review-service data/reviews.json")
    ap.add_argument("--seed-dir", type=Path, default=DEFAULT_SEED_DIR)
    ap.add_argument("--check", action="store_true", help="write nothing; exit 1 if products.json is out of sync")
    args = ap.parse_args()

    path = args.seed_dir / "products.json"
    products = json.loads(path.read_text(encoding="utf-8"))
    reviews = json.loads(args.reviews.read_text(encoding="utf-8"))
    try:
        changed = sync(products, reviews)
    except ValueError as e:
        print(f"error: {e}", file=sys.stderr)
        return 1
    print(f"{len(reviews)} reviews, {len(products)} products, {changed} product(s) {'out of sync' if args.check else 'updated'}")
    if args.check:
        return 1 if changed else 0
    path.write_text(json.dumps(products, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    return 0


if __name__ == "__main__":
    sys.exit(main())
