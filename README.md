# Stockroom

A Go HTTP service for a single warehouse. A scanner can retry a delivery after a timeout and receive the original receipt. Two pickers can request the last item at the same time, and only one gets it.

## Run locally

Requires Go 1.22 or later. The default listener is `127.0.0.1:8080`. With no database configured, balances and receipts are kept in memory until the process exits.

```sh
export STOCKROOM_READ_KEY=replace-with-reader-secret
export STOCKROOM_WRITE_KEY=replace-with-writer-secret
go run ./cmd/stockroom
```

### Run with PostgreSQL

With the two key variables above exported, a local PostgreSQL server running and a role allowed to create databases, run:

```sh
createdb stockroom
export DATABASE_URL='postgres:///stockroom?host=/var/run/postgresql&sslmode=disable'
go run ./cmd/stockroom
```

For a TCP connection, set `DATABASE_URL` to your server's PostgreSQL URL. Startup applies pending migrations before opening the HTTP listener. The database role needs permission to create tables in its schema. Balance updates and movement receipts commit together. Each write locks its request key first, then its SKU row.

In a second terminal with the same key variables exported, receive four packs of filters, issue one, then replay the delivery. Run these commands against an empty database or a fresh in-memory process.

```sh
move() {
  curl -si http://127.0.0.1:8080/v1/movements \
    -H "Authorization: Bearer $STOCKROOM_WRITE_KEY" -H 'Content-Type: application/json' -H "Idempotency-Key: $1" --data "$2"
}
delivery='{"sku":"FILTER-02","kind":"receive","quantity":4}'
move dock-17 "$delivery"                                               # 201, balance 4
move pick-91 '{"sku":"FILTER-02","kind":"issue","quantity":1}'         # 201, balance 3
move dock-17 "$delivery"                                               # 200, original balance 4
curl -s -H "Authorization: Bearer $STOCKROOM_READ_KEY" http://127.0.0.1:8080/v1/stock/FILTER-02                      # current balance 3
```

## API

| Request | Result |
| --- | --- |
| `GET /healthz` | Process health |
| `GET /v1/stock/{sku}` | Current balance, or 404 for an unknown SKU |
| `GET /v1/stock/{sku}/movements` | Movement history, newest ledger ID first |
| `POST /v1/movements` | Apply a receipt or issue, or replay the original receipt |

The two API keys must be distinct. Replace the example values with independent secrets. Both roles can read balances and history. Only the writer can post movements. Missing or invalid bearer keys return 401, and writes with a reader key return 403. Health checks are public.

History accepts a page size of 1–100 (default 20). Pass the returned `next_cursor` to the same SKU for the next page. The last page omits it. New movements added between requests appear on a fresh first page. Ledger IDs break timestamp ties and duplicate deliveries appear only once.

```sh
curl -s -H "Authorization: Bearer $STOCKROOM_READ_KEY" \
  'http://127.0.0.1:8080/v1/stock/FILTER-02/movements?limit=1'
```

Every movement needs one `Idempotency-Key` header. Keys are unique across the warehouse and accept 1–128 ASCII letters, digits, periods, underscores, colons, or hyphens. Use the same key when retrying the same logical movement. Reusing a successful key with a different payload returns 409. An issue that exceeds available stock also returns 409 and leaves the key available for retry.

Quantities range from 1 to 1,000,000 per movement. A SKU begins with an uppercase ASCII letter or digit and can contain uppercase letters, digits, underscores and hyphens, up to 32 characters. A successful issue that empties the shelf leaves a known SKU with balance zero.

## Verify

```sh
go test -race ./...
go vet ./...
```

Run the database tests against a development database using a role with permission to create schemas. Each test run creates and drops its own schema.

```sh
STOCKROOM_TEST_DATABASE_URL="$DATABASE_URL" go test -race -count=1 ./internal/inventory
```

The concurrency tests cover simultaneous retries and buyers competing for the final unit. The HTTP tests exercise the same handlers served by the binary.

## Five-day build

| Milestone | Acceptance check |
| --- | --- |
| 1. Stock movements and idempotent HTTP writes | Retry a receipt after a later issue and compare the historical receipt with the live balance. |
| 2. PostgreSQL persistence and migrations | Restart the service, replay a request, and race two writers on separate database connections. |
| 3. Authentication and stock history | Give a reader and a writer distinct permissions. Page through a SKU's movement history in stable order. |
| 4. Inventory reconciliation worker | Rebuild balances from the movement ledger and report discrepancies. Cancel the worker cleanly and test concurrent writes. |
| 5. Reproducible release | Start the stack with Docker Compose and exercise the demo against PostgreSQL. Run CI, including database integration tests, and document measured results. |
