-- General store schema: departments, richer product columns and the seed bookkeeping row.
-- tags/images are TEXT[]: ordered lists of plain strings, scanned by pgx straight into []string,
-- no nested structure that would need JSONB.

-- +goose Up
CREATE TABLE IF NOT EXISTS departments (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    sort_order  INT  NOT NULL DEFAULT 0
);

ALTER TABLE products
    ADD COLUMN IF NOT EXISTS brand           TEXT    NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS department      TEXT    REFERENCES departments (id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS source_category TEXT    NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS tags            TEXT[]  NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS images          TEXT[]  NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS thumbnail       TEXT    NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS rating          DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (rating >= 0 AND rating <= 5),
    ADD COLUMN IF NOT EXISTS review_count    INT     NOT NULL DEFAULT 0 CHECK (review_count >= 0),
    ADD COLUMN IF NOT EXISTS stock           INT     NOT NULL DEFAULT 0 CHECK (stock >= 0),
    ADD COLUMN IF NOT EXISTS weight_grams    INT     NOT NULL DEFAULT 0 CHECK (weight_grams >= 0);

CREATE INDEX IF NOT EXISTS products_department_idx ON products (department);

-- One row: checksum of the seed files last applied, so an unchanged seed is skipped on start-up.
CREATE TABLE IF NOT EXISTS catalog_seed (
    id            INT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    sha256        TEXT        NOT NULL,
    product_count INT         NOT NULL,
    applied_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS catalog_seed;
DROP INDEX IF EXISTS products_department_idx;
ALTER TABLE products
    DROP COLUMN IF EXISTS brand,
    DROP COLUMN IF EXISTS department,
    DROP COLUMN IF EXISTS source_category,
    DROP COLUMN IF EXISTS tags,
    DROP COLUMN IF EXISTS images,
    DROP COLUMN IF EXISTS thumbnail,
    DROP COLUMN IF EXISTS rating,
    DROP COLUMN IF EXISTS review_count,
    DROP COLUMN IF EXISTS stock,
    DROP COLUMN IF EXISTS weight_grams;
DROP TABLE IF EXISTS departments;
