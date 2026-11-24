package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/IdaemonI/stockroom-go/internal/inventory"
)

func TestRunRetriesAndCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var output bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&output, nil))
	entered := make(chan struct{})
	done := make(chan struct{})
	calls := 0
	go func() {
		defer close(done)
		run(ctx, time.Millisecond, func(ctx context.Context) ([]discrepancy, error) {
			calls++
			switch calls {
			case 1:
				return nil, errors.New("temporary failure")
			case 2:
				return []discrepancy{{SKU: "X"}}, nil
			default:
				close(entered)
				<-ctx.Done()
				return nil, ctx.Err()
			}
		}, log)
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not repeat")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("active scan did not stop")
	}
	lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("unexpected worker output: %s", lines)
	}
	var failed struct{ Level, Error string }
	var warned struct {
		Level         string
		MismatchCount int `json:"mismatch_count"`
		Discrepancies []discrepancy
	}
	if err := json.Unmarshal(lines[0], &failed); err != nil || failed.Level != "ERROR" || failed.Error != "temporary failure" {
		t.Fatalf("failure log: %s", lines[0])
	}
	if err := json.Unmarshal(lines[1], &warned); err != nil || warned.Level != "WARN" || warned.MismatchCount != 1 || len(warned.Discrepancies) != 1 || warned.Discrepancies[0].SKU != "X" {
		t.Fatalf("mismatch log: %s", lines[1])
	}
}

func TestCompareCommittedSnapshot(t *testing.T) {
	dsn := os.Getenv("STOCKROOM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set STOCKROOM_TEST_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	base, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := fmt.Sprintf("audit_test_%d", time.Now().UnixNano())
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
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := sql.Open("postgres", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = inventory.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := inventory.NewPostgres(db)
	if _, _, err = store.Move(ctx, "delivery", inventory.Command{Kind: inventory.Receive, Quantity: 5, SKU: "TEA"}); err != nil {
		t.Fatal(err)
	}

	// Hold a writer between the two halves of an issue. Audit via another
	// connection while that transaction is open, then again after its commit.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE stock_balances SET balance=3 WHERE sku='TEA'"); err != nil {
		t.Fatal(err)
	}
	clean := func() {
		t.Helper()
		d, err := compare(ctx, db)
		if err != nil || len(d) != 0 {
			t.Fatalf("expected clean snapshot: %+v, %v", d, err)
		}
	}
	clean()
	if _, err = tx.ExecContext(ctx, "INSERT INTO stock_movements (request_id,sku,kind,quantity,balance) VALUES ('pick','TEA','issue',2,3)"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	clean()
	// Exercise the production write path during repeated audit statements.
	finished := make(chan error, 1)
	go func() {
		for i := range 50 {
			_, _, err := store.Move(ctx, fmt.Sprintf("box-%d", i), inventory.Command{SKU: "BOX", Kind: inventory.Receive, Quantity: 1})
			if err != nil {
				finished <- err
				return
			}
		}
		finished <- nil
	}()
	for range 50 {
		clean()
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}

	if _, err = db.ExecContext(ctx, "UPDATE stock_balances SET balance=4 WHERE sku='TEA'"); err != nil {
		t.Fatal(err)
	}
	for range 2 { // A second scan must see the same unrepaired discrepancy.
		d, err := compare(ctx, db)
		if err != nil {
			t.Fatal(err)
		}
		got, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != `[{"sku":"TEA","stored":4,"expected":3}]` {
			t.Fatalf("audit result: %s", got)
		}
	}
}
