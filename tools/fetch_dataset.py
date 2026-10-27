#!/usr/bin/env python3
"""Build the curated store dataset ONCE from DummyJSON and write it as committed files.

This is a one-off maintainer tool. Nothing in the running service calls an external API:
the service reads the committed seed files and the frontend serves the committed images.

Requirements: Python 3.10+ (standard library) plus Pillow for the image conversion.
Use a virtualenv, do not install system-wide:

    python3 -m venv .venv && .venv/bin/pip install pillow==13.0.0

Usage (from the repo root of product-catalog-service):

    .venv/bin/python tools/fetch_dataset.py \
        --images-dir ../frontend/static/img/products \
        [--out-dir data/seed] [--cache-dir /tmp/dataset-cache] [--skip-existing-images]

What it does:
  1. Downloads https://dummyjson.com/products?limit=0 and /products/categories (cached in --cache-dir).
  2. Keeps only the store-like source categories (see DEPARTMENTS) and groups them into departments.
  3. Converts every product to the seed format (exact cent prices, no reviewer e-mails, ...).
  4. Downloads up to MAX_IMAGES images plus the thumbnail per product (sequentially, with a pause),
     converts them to WebP (long side <= 1000 px, thumbnail 600 px made from the full photo, quality 88) into --images-dir.
  5. Drops products whose first photo is byte-identical to an already kept product.
  6. Writes <out-dir>/products.json and <out-dir>/categories.json.

The fetched content is treated as data only. Re-running with a warm cache is deterministic.
"""
from __future__ import annotations

import argparse
import hashlib
import io
import json
import re
import sys
import time
import unicodedata
import urllib.request
from decimal import Decimal
from pathlib import Path

from PIL import Image

SOURCE_PRODUCTS = "https://dummyjson.com/products?limit=0"
SOURCE_CATEGORIES = "https://dummyjson.com/products/categories"
IMAGE_HOST_PREFIX = "https://cdn.dummyjson.com/"
USER_AGENT = "platform-demo-dataset-builder/1.0 (one-off fetch for a demo shop)"

MAX_IMAGES = 2          # product images kept per product
IMAGE_LONG_SIDE = 1000  # px (the source images are 1000 px: kept at full size, never upscaled)
THUMB_LONG_SIDE = 600   # px (card images, sharp on 2x screens; made from the full-size photo, not from the 300 px source thumbnail)
WEBP_QUALITY = 88
PAUSE_SECONDS = 0.25    # between image downloads, to be polite to the CDN
ID_MAX_LEN = 40
PICTURE_PREFIX = "/static/img/products/"

# Department id -> (name, description, [(source category, product type)]).
# The product type keeps the old free-form category words ("clothing", "footwear", "decor", ...)
# so existing consumers of the comma-separated categories column still find familiar values.
DEPARTMENTS = {
    "women": ("Women", "Dresses, tops, shoes and handbags for women.",
              [("tops", "clothing"), ("womens-dresses", "clothing"),
               ("womens-shoes", "footwear"), ("womens-bags", "bags")]),
    "men": ("Men", "Shirts and shoes for men.",
            [("mens-shirts", "clothing"), ("mens-shoes", "footwear")]),
    "accessories": ("Accessories", "Watches, jewellery and sunglasses.",
                    [("mens-watches", "watches"), ("womens-watches", "watches"),
                     ("womens-jewellery", "jewellery"), ("sunglasses", "sunglasses")]),
    "beauty": ("Beauty", "Make-up, skin care and fragrances.",
               [("beauty", "makeup"), ("skin-care", "skin-care"), ("fragrances", "fragrances")]),
    "home": ("Home", "Furniture and home decoration.",
             [("furniture", "furniture"), ("home-decoration", "decor")]),
    "kitchen": ("Kitchen", "Cookware, utensils, small appliances and tableware.",
                [("kitchen-accessories", "kitchen")]),
    "sports": ("Sports", "Balls, bats, rackets and other sports gear.",
               [("sports-accessories", "sports")]),
}

# Source titles that are visibly truncated; fixed by hand (documented in SOURCES.md).
TITLE_FIXES = {
    "Chanel Coco Noir Eau De": "Chanel Coco Noir Eau de Parfum",
    "Dolce Shine Eau de": "Dolce Shine Eau de Parfum",
    "Gucci Bloom Eau de": "Gucci Bloom Eau de Parfum",
}

# The source weight has no documented unit (values are small integers 1-10).
# We ASSUME kilograms and convert to grams. Documented in SOURCES.md.
WEIGHT_UNIT_TO_GRAMS = 1000


def fetch(url: str, cache: Path) -> bytes:
    if cache.exists():
        return cache.read_bytes()
    req = urllib.request.Request(url, headers={"User-Agent": USER_AGENT})
    with urllib.request.urlopen(req, timeout=60) as resp:
        if resp.status != 200:
            raise RuntimeError(f"GET {url}: HTTP {resp.status}")
        data = resp.read()
    cache.parent.mkdir(parents=True, exist_ok=True)
    cache.write_bytes(data)
    time.sleep(PAUSE_SECONDS)
    return data


def slugify(text: str) -> str:
    text = unicodedata.normalize("NFKD", text).encode("ascii", "ignore").decode()
    text = text.lower().replace("&", " and ").replace("'", "")
    return re.sub(r"[^a-z0-9]+", "-", text).strip("-")


def make_id(title: str, source_id: int) -> str:
    suffix = f"-{source_id}"
    slug = slugify(title)
    room = ID_MAX_LEN - len(suffix)
    if len(slug) > room:
        slug = slug[:room].rsplit("-", 1)[0] if "-" in slug[:room] else slug[:room]
    return f"{slug.strip('-')}{suffix}"


def money(price: Decimal) -> dict:
    cents = price * 100
    if cents != cents.to_integral_value():
        raise ValueError(f"price {price} is not a whole number of cents")
    cents = int(cents)
    return {"currency_code": "USD", "units": cents // 100, "nanos": (cents % 100) * 10_000_000}


def to_webp(raw: bytes, long_side: int, dest: Path, skip_existing: bool = False) -> None:
    if skip_existing and dest.is_file():
        return
    with Image.open(io.BytesIO(raw)) as img:
        img.load()
        img = img.convert("RGBA") if img.mode in ("RGBA", "LA", "P") else img.convert("RGB")
        img.thumbnail((long_side, long_side), Image.Resampling.LANCZOS)
        dest.parent.mkdir(parents=True, exist_ok=True)
        img.save(dest, "WEBP", quality=WEBP_QUALITY, method=4)


def image_cache_path(cache_dir: Path, url: str) -> Path:
    if not url.startswith(IMAGE_HOST_PREFIX):
        raise ValueError(f"unexpected image host: {url}")
    rel = url[len(IMAGE_HOST_PREFIX):]
    if ".." in rel.split("/"):
        raise ValueError(f"bad image path: {url}")
    return cache_dir / "images" / rel


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--images-dir", required=True, type=Path, help="frontend static/img/products directory")
    ap.add_argument("--out-dir", type=Path, default=Path(__file__).resolve().parent.parent / "data" / "seed")
    ap.add_argument("--cache-dir", type=Path, default=Path(".dataset-cache"),
                    help="download cache (keep it outside the repo)")
    ap.add_argument("--skip-existing-images", action="store_true",
                    help="do not re-encode WebP files that already exist (WebP encoding is slow)")
    args = ap.parse_args()

    source_to_dept = {}
    for dept_id, (_, _, sources) in DEPARTMENTS.items():
        for src, ptype in sources:
            source_to_dept[src] = (dept_id, ptype)

    raw = fetch(SOURCE_PRODUCTS, args.cache_dir / "products.json")
    products_src = json.loads(raw, parse_float=Decimal)["products"]
    categories_src = json.loads(fetch(SOURCE_CATEGORIES, args.cache_dir / "categories.json"))
    known = {c["slug"] for c in categories_src}
    missing = set(source_to_dept) - known
    if missing:
        print(f"warning: source categories not offered by the API: {sorted(missing)}", file=sys.stderr)

    out = []
    seen_photos: dict[str, str] = {}
    for p in sorted(products_src, key=lambda x: int(x["id"])):
        if p["category"] not in source_to_dept:
            continue
        # Some source products are re-listed in another category with the very same photos
        # (e.g. a Rolex in mens-watches and womens-watches). Keep the first (lowest source id).
        first_url = p["images"][0]
        digest = hashlib.sha256(fetch(first_url, image_cache_path(args.cache_dir, first_url))).hexdigest()
        if digest in seen_photos:
            print(f"drop source id {p['id']} {p['title']!r}: same photo as {seen_photos[digest]}", file=sys.stderr)
            continue
        dept_id, ptype = source_to_dept[p["category"]]
        title = TITLE_FIXES.get(p["title"], p["title"]).strip()
        pid = make_id(title, int(p["id"]))
        seen_photos[digest] = pid

        images = []
        for n, url in enumerate(p.get("images", [])[:MAX_IMAGES], start=1):
            name = f"{pid}-{n}.webp"
            to_webp(fetch(url, image_cache_path(args.cache_dir, url)), IMAGE_LONG_SIDE, args.images_dir / name,
                    args.skip_existing_images)
            images.append(PICTURE_PREFIX + name)
        thumb_name = f"{pid}-thumb.webp"
        thumb_url = p["images"][0]  # the source thumbnail is only 300 px: too soft for cards on 2x screens
        to_webp(fetch(thumb_url, image_cache_path(args.cache_dir, thumb_url)), THUMB_LONG_SIDE,
                args.images_dir / thumb_name, args.skip_existing_images)

        categories = []
        for c in (dept_id, ptype, p["category"]):
            if c not in categories:
                categories.append(c)

        out.append({
            "id": pid,
            "name": title,
            "description": p["description"].strip(),
            "brand": (p.get("brand") or "").strip(),
            "category": dept_id,
            "source_category": p["category"],
            "categories": categories,
            "tags": [str(t).strip().lower() for t in p.get("tags", [])],
            "price_usd": money(p["price"]),
            "rating": float(p["rating"]),
            "stock": int(p["stock"]),
            "weight_grams": int(Decimal(p["weight"]) * WEIGHT_UNIT_TO_GRAMS),
            "images": images,
            "thumbnail": PICTURE_PREFIX + thumb_name,
            # reviewerEmail is dropped on purpose: no e-mail addresses in the dataset.
            "reviews": [{
                "reviewer_name": r["reviewerName"],
                "rating": int(r["rating"]),
                "comment": r["comment"],
                "date": r["date"],
            } for r in p.get("reviews", [])],
            "source_id": int(p["id"]),
        })
        print(f"{pid}: {len(images)} image(s) + thumb", file=sys.stderr)

    out.sort(key=lambda x: (list(DEPARTMENTS).index(x["category"]), x["source_category"], x["source_id"]))
    categories_out = [{
        "id": dept_id,
        "name": name,
        "description": desc,
        "source_categories": [s for s, _ in sources],
    } for dept_id, (name, desc, sources) in DEPARTMENTS.items()]

    args.out_dir.mkdir(parents=True, exist_ok=True)
    for fname, data in (("products.json", out), ("categories.json", categories_out)):
        (args.out_dir / fname).write_text(json.dumps(data, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    print(f"wrote {len(out)} products, {len(categories_out)} departments to {args.out_dir}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
