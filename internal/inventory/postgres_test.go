package inventory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"
)

func TestPostgresMovements(t *testing.T) {
	dsn := os.Getenv("STOCKROOM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set STOCKROOM_TEST_DATABASE_URL to run PostgreSQL tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	base, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := fmt.Sprintf("stockroom_test_%d", time.Now().UnixNano())
	if _, err = base.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := base.Exec("DROP SCHEMA " + schema + " CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	params := u.Query()
	params.Set("search_path", schema)
	u.RawQuery = params.Encode()
	open := func() *sql.DB {
		db, err := sql.Open("postgres", u.String())
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		return db
	}
	a, b := open(), open()
	defer a.Close()
	defer b.Close()
	start := make(chan struct{})
	migrated := make(chan error, 2)
	for _, db := range []*sql.DB{a, b} {
		go func(db *sql.DB) { <-start; migrated <- Migrate(ctx, db) }(db)
	}
	close(start)
	for range 2 {
		if err := <-migrated; err != nil {
			t.Fatal(err)
		}
	}

	type result struct {
		receipt Receipt
		replay  bool
		err     error
	}
	race := func(keys [2]string, cmd Command) []result {
		gate := make(chan struct{})
		ch := make(chan result, 2)
		for i, db := range []*sql.DB{a, b} {
			go func(db *sql.DB, key string) {
				<-gate
				r, again, err := NewPostgres(db).Move(ctx, key, cmd)
				ch <- result{r, again, err}
			}(db, keys[i])
		}
		close(gate)
		return []result{<-ch, <-ch}
	}
	receive := Command{SKU: "FILTER", Kind: Receive, Quantity: 1}
	retries := race([2]string{"delivery", "delivery"}, receive)
	if retries[0].err != nil || retries[1].err != nil || retries[0].replay == retries[1].replay || retries[0].receipt != retries[1].receipt || retries[0].receipt.Balance != 1 {
		t.Fatalf("retries: %+v", retries)
	}
	picks := race([2]string{"pick-A", "pick-B"}, Command{SKU: "FILTER", Kind: Issue, Quantity: 1})
	var success, conflict int
	for _, p := range picks {
		switch {
		case p.err == nil && !p.replay && p.receipt.Balance == 0:
			success++
		case errors.Is(p.err, ErrInsufficient):
			conflict++
		default:
			t.Fatalf("unexpected pick: %+v", p)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("picks: %+v", picks)
	}
	store := NewPostgres(b)
	if _, _, err := store.Move(ctx, "delivery", Command{SKU: "OTHER", Kind: Receive, Quantity: 1}); !errors.Is(err, ErrConflict) {
		t.Fatalf("key conflict: %v", err)
	}
	r, replay, err := store.Move(ctx, "delivery", receive)
	if err != nil || !replay || r != retries[0].receipt {
		t.Fatalf("historical receipt: %+v %v %v", r, replay, err)
	}
	var count int
	if err := a.QueryRowContext(ctx, "SELECT count(*) FROM stock_movements").Scan(&count); err != nil || count != 2 {
		t.Fatalf("ledger count %d: %v", count, err)
	}

	// Force equal timestamps across page boundaries and include another SKU.
	for _, key := range []string{"third", "fourth"} {
		if _, _, err := store.Move(ctx, key, receive); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := store.Move(ctx, "other", Command{SKU: "OTHER", Kind: Receive, Quantity: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ExecContext(ctx, "UPDATE stock_movements SET created_at = '2025-01-01T00:00:00Z'"); err != nil {
		t.Fatal(err)
	}
	first, err := store.History(ctx, "FILTER", 0, 2)
	if err != nil || len(first) != 2 || first[0].RequestID != "fourth" || first[1].RequestID != "third" {
		t.Fatalf("first page: %+v %v", first, err)
	}
	second, err := store.History(ctx, "FILTER", first[1].ID, 2)
	if err != nil || len(second) != 2 || second[1].RequestID != "delivery" || second[0].ID >= first[1].ID || !second[0].CreatedAt.Equal(first[1].CreatedAt) {
		t.Fatalf("second page: %+v %v", second, err)
	}

	// Make the receipt insert fail after the balance has been updated.
	if _, err := a.ExecContext(ctx, "ALTER TABLE stock_movements ADD CHECK (request_id <> 'fault')"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Move(ctx, "fault", receive); err == nil {
		t.Fatal("expected receipt write to fail")
	}
	if balance, err := store.Balance(ctx, "FILTER"); err != nil || balance != 2 {
		t.Fatalf("balance after rollback: %d %v", balance, err)
	}
}
