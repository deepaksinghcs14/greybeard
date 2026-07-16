CREATE TABLE orders (
    id         uuid PRIMARY KEY,
    total      numeric NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS order_items (
    id       uuid PRIMARY KEY,
    order_id uuid REFERENCES orders (id),
    sku      text NOT NULL
);
