#!/usr/bin/env python3
"""Validate the committed store dataset (data/seed) against the committed images.

Standard library only. Exit code 0 = valid, 1 = at least one error.

    python3 tools/validate_dataset.py --images-dir ../frontend/static/img/products [--seed-dir data/seed]
"""
from __future__ import annotations

import argparse
import json
import re
import sys
from collections import Counter
from pathlib import Path

ID_RE = re.compile(r"^[a-z0-9]+(?:-[a-z0-9]+)*$")
EMAIL_RE = re.compile(r"[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}")
PICTURE_PREFIX = "/static/img/products/"
REQUIRED = {
    "id": str, "name": str, "description": str, "brand": str, "category": str,
    "source_category": str, "categories": list, "tags": list, "price_usd": dict,
    "rating": float, "stock": int, "weight_grams": int, "images": list,
    "thumbnail": str, "reviews": list,
}
REVIEW_REQUIRED = {"reviewer_name": str, "rating": int, "comment": str, "date": str}
MAX_TOTAL_IMAGE_BYTES = 10 * 1024 * 1024


def walk_strings(obj):
    if isinstance(obj, str):
        yield obj
    elif isinstance(obj, dict):
        for k, v in obj.items():
            yield k
            yield from walk_strings(v)
    elif isinstance(obj, list):
        for v in obj:
            yield from walk_strings(v)


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--images-dir", required=True, type=Path)
    ap.add_argument("--seed-dir", type=Path, default=Path(__file__).resolve().parent.parent / "data" / "seed")
    args = ap.parse_args()

    errors: list[str] = []
    products = json.loads((args.seed_dir / "products.json").read_text(encoding="utf-8"))
    departments = json.loads((args.seed_dir / "categories.json").read_text(encoding="utf-8"))

    dept_ids = [d.get("id") for d in departments]
    if len(set(dept_ids)) != len(dept_ids):
        errors.append("categories.json: duplicate department ids")
    for d in departments:
        for f in ("id", "name", "description", "source_categories"):
            if not d.get(f):
                errors.append(f"categories.json: department {d.get('id')!r} missing {f}")
    src_to_dept = {s: d["id"] for d in departments for s in d.get("source_categories", [])}

    ids = Counter(p.get("id") for p in products)
    for pid, n in ids.items():
        if n > 1:
            errors.append(f"duplicate id {pid!r} ({n}x)")

    referenced: set[str] = set()
    for p in products:
        pid = p.get("id", "<no id>")
        for field, typ in REQUIRED.items():
            if field not in p:
                errors.append(f"{pid}: missing {field}")
            elif not isinstance(p[field], typ) or (typ is int and isinstance(p[field], bool)):
                errors.append(f"{pid}: {field} should be {typ.__name__}, got {type(p[field]).__name__}")
        if not isinstance(pid, str) or not ID_RE.match(pid) or len(pid) > 40:
            errors.append(f"{pid}: id must be lower-case URL-safe slug <= 40 chars")
        for f in ("name", "description"):
            if not str(p.get(f, "")).strip():
                errors.append(f"{pid}: empty {f}")
        if p.get("category") not in dept_ids:
            errors.append(f"{pid}: unknown department {p.get('category')!r}")
        if src_to_dept.get(p.get("source_category")) != p.get("category"):
            errors.append(f"{pid}: source_category {p.get('source_category')!r} not in department {p.get('category')!r}")
        cats = p.get("categories") or []
        if not cats or any(not isinstance(c, str) or c != c.lower() or "," in c or not c.strip() for c in cats):
            errors.append(f"{pid}: categories must be non-empty lower-case strings without commas")

        price = p.get("price_usd") or {}
        units, nanos = price.get("units"), price.get("nanos")
        if price.get("currency_code") != "USD":
            errors.append(f"{pid}: currency_code must be USD")
        if not isinstance(units, int) or not isinstance(nanos, int):
            errors.append(f"{pid}: price units/nanos must be int")
        else:
            if units < 0 or not 0 <= nanos < 1_000_000_000 or nanos % 10_000_000:
                errors.append(f"{pid}: price {units}.{nanos} out of range or not whole cents")
            if units == 0 and nanos == 0:
                errors.append(f"{pid}: price is zero")
            if units > 100_000:
                errors.append(f"{pid}: price {units} implausibly high")
        rating = p.get("rating")
        if not isinstance(rating, (int, float)) or not 0 <= rating <= 5:
            errors.append(f"{pid}: rating {rating!r} not in 0..5")
        stock = p.get("stock")
        if not isinstance(stock, int) or stock < 0:
            errors.append(f"{pid}: stock {stock!r} must be int >= 0")
        weight = p.get("weight_grams")
        if not isinstance(weight, int) or weight <= 0:
            errors.append(f"{pid}: weight_grams {weight!r} must be int > 0")

        imgs = list(p.get("images") or [])
        if not imgs:
            errors.append(f"{pid}: no images")
        for path in imgs + [p.get("thumbnail", "")]:
            if not isinstance(path, str) or not path.startswith(PICTURE_PREFIX) or "/" in path[len(PICTURE_PREFIX):]:
                errors.append(f"{pid}: bad image path {path!r}")
                continue
            name = path[len(PICTURE_PREFIX):]
            referenced.add(name)
            f = args.images_dir / name
            if not f.is_file() or f.stat().st_size == 0:
                errors.append(f"{pid}: image file missing: {f}")
            elif f.read_bytes()[:4] != b"RIFF" or f.read_bytes()[8:12] != b"WEBP":
                errors.append(f"{pid}: not a WebP file: {f}")

        for i, r in enumerate(p.get("reviews") or []):
            for field, typ in REVIEW_REQUIRED.items():
                if not isinstance(r.get(field), typ):
                    errors.append(f"{pid}: review {i} field {field} missing or not {typ.__name__}")
            if isinstance(r.get("rating"), int) and not 1 <= r["rating"] <= 5:
                errors.append(f"{pid}: review {i} rating out of 1..5")
            extra = set(r) - set(REVIEW_REQUIRED)
            if extra:
                errors.append(f"{pid}: review {i} has unexpected fields {sorted(extra)}")

    for fname, data in (("products.json", products), ("categories.json", departments)):
        for s in walk_strings(data):
            m = EMAIL_RE.search(s)
            if m:
                errors.append(f"{fname}: e-mail-like string found: {m.group(0)!r}")

    # Every WebP in the images dir must belong to a product (the old .jpg demo images are ignored).
    for f in sorted(args.images_dir.glob("*.webp")):
        if f.name not in referenced:
            errors.append(f"unreferenced image file: {f.name}")

    total = sum((args.images_dir / n).stat().st_size for n in referenced if (args.images_dir / n).is_file())
    per_dept = Counter(p.get("category") for p in products)
    per_src = Counter(p.get("source_category") for p in products)

    print(f"products: {len(products)}")
    print("per department:")
    for d in departments:
        print(f"  {d['id']:<12} {per_dept.get(d['id'], 0):>4}   " +
              ", ".join(f"{s}={per_src.get(s, 0)}" for s in d["source_categories"]))
    print(f"referenced image files: {len(referenced)}  total size: {total} bytes ({total / 1024 / 1024:.2f} MiB)")
    if total > MAX_TOTAL_IMAGE_BYTES:
        errors.append(f"total image size {total} exceeds {MAX_TOTAL_IMAGE_BYTES}")

    if errors:
        print(f"FAILED: {len(errors)} error(s)")
        for e in errors:
            print(f"  - {e}")
        return 1
    print("OK: dataset is valid")
    return 0


if __name__ == "__main__":
    sys.exit(main())
