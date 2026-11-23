CREATE TABLE stock_balances (
    sku text PRIMARY KEY,
    balance bigint NOT NULL CHECK (balance >= 0)
);

CREATE TABLE stock_movements (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    request_id text NOT NULL UNIQUE,
    sku text NOT NULL REFERENCES stock_balances (sku),
    kind text NOT NULL CHECK (kind IN ('receive', 'issue')),
    quantity bigint NOT NULL CHECK (quantity BETWEEN 1 AND 1000000),
    balance bigint NOT NULL CHECK (balance >= 0),
    created_at timestamptz NOT NULL DEFAULT now()
);
