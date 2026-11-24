// Command reconcile audits PostgreSQL stock balances against committed movements.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/lib/pq"
)

type discrepancy struct {
	SKU      string       `json:"sku"`
	Stored   *int64       `json:"stored"`
	Expected *json.Number `json:"expected"`
}

// PostgreSQL uses one MVCC snapshot for both sides of this SELECT. Use the
// signed quantities rather than the historical balance copied onto receipts.
const compareSQL = `
WITH ledger AS (
    SELECT sku, sum(CASE kind WHEN 'receive' THEN quantity ELSE -quantity END) AS expected
    FROM stock_movements
    GROUP BY sku
)
SELECT sku, b.balance, l.expected::text
FROM stock_balances b FULL JOIN ledger l USING (sku)
WHERE b.balance IS DISTINCT FROM l.expected
ORDER BY sku`

func compare(ctx context.Context, db *sql.DB) ([]discrepancy, error) {
	rows, err := db.QueryContext(ctx, compareSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]discrepancy, 0)
	for rows.Next() {
		var d discrepancy
		if err := rows.Scan(&d.SKU, &d.Stored, &d.Expected); err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	return result, rows.Err()
}

// Wait for the interval after each scan so expensive scans cannot overlap.
func run(ctx context.Context, interval time.Duration, scan func(context.Context) ([]discrepancy, error), log *slog.Logger) {
	for ctx.Err() == nil {
		diffs, err := scan(ctx)
		if ctx.Err() != nil {
			return
		}
		switch {
		case err != nil:
			log.Error("inventory reconciliation failed", "error", err)
		case len(diffs) > 0:
			log.Warn("inventory reconciliation", "mismatch_count", len(diffs), "discrepancies", diffs)
		default:
			log.Info("inventory reconciliation", "mismatch_count", 0)
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func main() {
	interval := flag.Duration("interval", time.Minute, "delay between reconciliation scans")
	flag.Parse()
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := start(*interval, log); err != nil {
		log.Error("worker stopped", "error", err)
		os.Exit(1)
	}
}

func start(interval time.Duration, log *slog.Logger) error {
	if interval <= 0 {
		return errors.New("interval must be positive")
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL is required")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	run(ctx, interval, func(ctx context.Context) ([]discrepancy, error) { return compare(ctx, db) }, log)
	return nil
}
