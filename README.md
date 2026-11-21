# Stockroom

A Go HTTP service for a single warehouse. A scanner can retry a delivery after a timeout and receive the original receipt. Two pickers can request the last item at the same time, and only one gets it.

## Run locally

Requires Go 1.22 or later. The default listener is `127.0.0.1:8080`. The first version keeps balances and receipts in memory. Restarting the process clears both.

```sh
go run ./cmd/stockroom
```

From a second terminal, receive four packs of filters, issue one, then replay the delivery. Run these commands against a fresh process.

```sh
move() {
  curl -si http://127.0.0.1:8080/v1/movements \
    -H 'Content-Type: application/json' -H "Idempotency-Key: $1" --data "$2"
}
delivery='{"sku":"FILTER-02","kind":"receive","quantity":4}'
move dock-17 "$delivery"                                               # 201, balance 4
move pick-91 '{"sku":"FILTER-02","kind":"issue","quantity":1}'         # 201, balance 3
move dock-17 "$delivery"                                               # 200, original balance 4
curl -s http://127.0.0.1:8080/v1/stock/FILTER-02                      # current balance 3
```

## API

| Request | Result |
| --- | --- |
| `GET /healthz` | Process health |
| `GET /v1/stock/{sku}` | Current balance, or 404 for an unknown SKU |
| `POST /v1/movements` | Apply a receipt or issue, or replay the original receipt |

Every movement needs one `Idempotency-Key` header. Keys are unique across the warehouse and accept 1–128 ASCII letters, digits, periods, underscores, colons, or hyphens. Use the same key when retrying the same logical movement. Reusing a successful key with a different payload returns 409. An issue that exceeds available stock also returns 409 and leaves the key available for retry.

Quantities range from 1 to 1,000,000 per movement. A SKU begins with an uppercase ASCII letter or digit and can contain uppercase letters, digits, underscores and hyphens, up to 32 characters. A successful issue that empties the shelf leaves a known SKU with balance zero.

## Verify

```sh
go test -race ./...
go vet ./...
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
