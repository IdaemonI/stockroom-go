package inventory

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"

	_ "github.com/lib/pq"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrate applies numbered SQL files in lexical order. All statements and the
// migration record share a transaction, including on concurrent API startups.
func Migrate(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(847526001::bigint)"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS stockroom_migrations (name text PRIMARY KEY)"); err != nil {
		return err
	}
	files, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	for _, file := range files {
		var applied bool
		if err = tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM stockroom_migrations WHERE name = $1)", file.Name()).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		body, err := migrations.ReadFile("migrations/" + file.Name())
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, string(body)); err != nil {
			return fmt.Errorf("%s: %w", file.Name(), err)
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO stockroom_migrations (name) VALUES ($1)", file.Name()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type Postgres struct{ db *sql.DB }

func NewPostgres(db *sql.DB) *Postgres { return &Postgres{db: db} }

func (s *Postgres) Balance(ctx context.Context, sku string) (int64, error) {
	if !skuPattern.MatchString(sku) {
		return 0, fmt.Errorf("%w: invalid SKU", ErrInvalid)
	}
	var balance int64
	err := s.db.QueryRowContext(ctx, "SELECT balance FROM stock_balances WHERE sku = $1", sku).Scan(&balance)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return balance, err
}

func (s *Postgres) Move(ctx context.Context, key string, cmd Command) (Receipt, bool, error) {
	if err := validateCommand(key, cmd); err != nil {
		return Receipt{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return Receipt{}, false, err
	}
	defer tx.Rollback()

	// Lock the request before the SKU. Equal keys always serialize, even if
	// clients disagree about the SKU. A hash collision only adds contention.
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(7319, hashtext($1))", key); err != nil {
		return Receipt{}, false, err
	}
	r := Receipt{RequestID: key}
	err = tx.QueryRowContext(ctx, "SELECT sku, kind, quantity, balance FROM stock_movements WHERE request_id = $1", key).Scan(&r.SKU, &r.Kind, &r.Quantity, &r.Balance)
	if err == nil {
		if r.SKU != cmd.SKU || r.Kind != cmd.Kind || r.Quantity != cmd.Quantity {
			return Receipt{}, false, ErrConflict
		}
		return r, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Receipt{}, false, err
	}

	if _, err = tx.ExecContext(ctx, "INSERT INTO stock_balances (sku,balance) VALUES ($1,0) ON CONFLICT (sku) DO NOTHING", cmd.SKU); err != nil {
		return Receipt{}, false, err
	}
	var balance int64
	if err = tx.QueryRowContext(ctx, "SELECT balance FROM stock_balances WHERE sku = $1 FOR UPDATE", cmd.SKU).Scan(&balance); err != nil {
		return Receipt{}, false, err
	}
	balance, err = nextBalance(balance, cmd)
	if err != nil {
		return Receipt{}, false, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE stock_balances SET balance = $2 WHERE sku = $1", cmd.SKU, balance); err != nil {
		return Receipt{}, false, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO stock_movements (request_id,sku,kind,quantity,balance) VALUES ($1,$2,$3,$4,$5)", key, cmd.SKU, cmd.Kind, cmd.Quantity, balance); err != nil {
		return Receipt{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return Receipt{}, false, err
	}
	return Receipt{RequestID: key, SKU: cmd.SKU, Kind: cmd.Kind, Quantity: cmd.Quantity, Balance: balance}, false, nil
}
