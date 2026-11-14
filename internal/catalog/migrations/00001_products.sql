-- Baseline: the products table exactly as the product-catalog-db image's init script (dump.sql) creates it.
-- IF NOT EXISTS makes this a no-op on a database that was initialised by that image, so both a plain
-- postgres and the existing image end up on the same schema.

-- +goose Up
CREATE TABLE IF NOT EXISTS products (
    id                      TEXT PRIMARY KEY,
    name                    TEXT NOT NULL,
    description             TEXT NOT NULL,
    picture                 TEXT NOT NULL,
    price_usd_currency_code TEXT NOT NULL DEFAULT 'USD',
    price_usd_units         BIGINT NOT NULL DEFAULT 0,
    price_usd_nanos         INT NOT NULL DEFAULT 0,
    categories              TEXT NOT NULL DEFAULT ''
);

-- +goose Down
DROP TABLE IF EXISTS products;
