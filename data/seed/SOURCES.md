# Dataset sources

## Source

| | |
|---|---|
| Products | https://dummyjson.com/products?limit=0 (194 products in 24 categories at retrieval) |
| Categories | https://dummyjson.com/products/categories |
| Images | `https://cdn.dummyjson.com/product-images/...` (the `images` and `thumbnail` URLs of each product) |
| Retrieved | 2026-10-27 (one-off run of `tools/fetch_dataset.py`) |
| Fallback | Fake Store API (https://fakestoreapi.com/products) was looked at but **not used** (see below) |

## Licence and terms, as found on 2026-10-27

- DummyJSON: the project repository https://github.com/Ovi/DummyJSON has a `LICENSE` file with the **MIT License**
  ("DummyJSON - Muhammad Ovi (Owais)"), and the README's "License" section says "MIT". The README describes the service
  as "a free REST API for generating placeholder JSON data".
- Separate terms of use for dummyjson.com: **not found** (https://dummyjson.com/terms and /terms-of-service return 404;
  no terms, licence or copyright text was found on the home page).
- Licence or provenance of the product **images** on cdn.dummyjson.com specifically: **not found**. The images are
  assumed to be covered by the project's MIT licence; this is an assumption, not a verified fact.
- Brand and product names (Rolex, Nike, Chanel, ...) are kept as they appear in the source; they are trademarks of
  their owners and are used here only as demo data.
- Fake Store API: repository https://github.com/keikaavousi/fake-store-api is MIT licensed (not used).

## Selection

Kept the 17 store-like source categories; dropped vehicle, motorcycle, smartphones, tablets, laptops,
mobile-accessories and groceries. DummyJSON has only 119 products in the kept categories, so the target of about 150
was not reached. Fake Store API would add only 14 store-like items (6 women's clothing, 4 men's clothing,
4 jewellery) without brand, stock, weight or reviews and with a different photo style, so it was not mixed in.

Two products were dropped because their photos are byte-identical to another kept product (same item re-listed in
another category): source id 191 "Rolex Cellini Moonphase" (womens-watches, same photos as id 96) and
source id 192 "Rolex Datejust Women" (same photos as id 97). Result: **117 products**.

| Department | Source categories (products) | Total |
|---|---|---|
| `women` | tops 5, womens-dresses 5, womens-shoes 5, womens-bags 5 | 20 |
| `men` | mens-shirts 5, mens-shoes 5 | 10 |
| `accessories` | mens-watches 6, womens-watches 3, womens-jewellery 3, sunglasses 5 | 17 |
| `beauty` | beauty 5, skin-care 3, fragrances 5 | 13 |
| `home` | furniture 5, home-decoration 5 | 10 |
| `kitchen` | kitchen-accessories 30 | 30 |
| `sports` | sports-accessories 17 | 17 |

Note: the source category `tops` actually holds dresses/frocks (Blue Frock, Tartan Dress, ...); it is kept in `women`.

## What was changed

- `id`: `<slug of the title>-<source id>` (lower-case ASCII, `&` -> `and`, cut at a word boundary so the id is at
  most 40 characters). Stable as long as the source id and the title stay the same.
- `price_usd`: computed exactly from the source price parsed as a decimal (cents), e.g. 9.99 ->
  `{units: 9, nanos: 990000000}`. `discountPercentage` is dropped.
- `weight_grams`: the source `weight` has **no documented unit** (integers 1-10 for every product, a bed and a
  mascara alike). It is **assumed to be kilograms** and multiplied by 1000. The values are not realistic.
- `categories`: `[department, product type, source category]` without duplicates, e.g.
  `["men", "footwear", "mens-shoes"]`. The product type re-uses the old free-form words where they fit
  (`clothing`, `footwear`, `accessories` as a department, `decor`, `kitchen`, `beauty`).
- `tags`: lower-cased.
- Reviews: kept `reviewerName`, `rating`, `comment`, `date`; **dropped `reviewerEmail`** (no e-mail address is stored).
- Truncated source titles fixed by hand: "Chanel Coco Noir Eau De", "Dolce Shine Eau de" and "Gucci Bloom Eau de"
  -> "... Eau de Parfum".
- Dropped fields: `sku`, `dimensions`, `warrantyInformation`, `shippingInformation`, `availabilityStatus`,
  `returnPolicy`, `minimumOrderQuantity`, `meta` (barcode, QR code URL, timestamps), `discountPercentage`.
- `source_id` (the DummyJSON id) is kept for traceability.

## Images

- Up to 2 product images per product (the source has 1-4), converted to WebP, long side at most 800 px, quality 80;
  plus one thumbnail per product from the source thumbnail (`<id>-thumb.webp`), long side at most 320 px. The source
  thumbnails are 300 px, so the committed thumbnails are 300 px (no upscaling).
- 304 files (187 product images + 117 thumbnails), 9,238,380 bytes (8.81 MiB) in total, committed in the frontend
  repository under `static/img/products/`.
