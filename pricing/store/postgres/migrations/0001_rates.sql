CREATE TABLE IF NOT EXISTS aws_products (
    service    text  NOT NULL,
    region     text  NOT NULL,
    sku        text  NOT NULL,
    attributes jsonb NOT NULL,
    PRIMARY KEY (service, region, sku)
);

CREATE INDEX IF NOT EXISTS aws_products_attributes ON aws_products USING gin (attributes);

CREATE TABLE IF NOT EXISTS aws_prices (
    sku         text    NOT NULL,
    unit        text    NOT NULL,
    begin_range numeric NOT NULL,
    end_range   text    NOT NULL,
    price       numeric NOT NULL,
    PRIMARY KEY (sku, begin_range)
);

CREATE TABLE IF NOT EXISTS gcp_skus (
    sku_id      text  PRIMARY KEY,
    service     text  NOT NULL,
    description text  NOT NULL,
    category    jsonb NOT NULL,
    regions     text[] NOT NULL,
    tiers       jsonb NOT NULL
);

CREATE INDEX IF NOT EXISTS gcp_skus_service ON gcp_skus (service);

CREATE TABLE IF NOT EXISTS ingests (
    vendor     text        NOT NULL,
    service    text        NOT NULL,
    region     text        NOT NULL,
    version    text        NOT NULL,
    fetched_at timestamptz NOT NULL,
    PRIMARY KEY (vendor, service, region)
);
