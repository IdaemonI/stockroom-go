package inventory

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestReplayReturnsOriginalBalance(t *testing.T) {
	store := NewStore()
	ctx := context.Background()
	delivery := Command{SKU: "FILTER-01", Kind: Receive, Quantity: 10}
	first, replay, err := store.Move(ctx, "delivery:1", delivery)
	if err != nil || replay || first.Balance != 10 {
		t.Fatalf("first delivery = %+v, replay=%v, err=%v", first, replay, err)
	}
	if _, _, err := store.Move(ctx, "sale:1", Command{delivery.SKU, Issue, 10}); err != nil {
		t.Fatal(err)
	}
	again, replay, err := store.Move(ctx, "delivery:1", delivery)
	if err != nil || !replay || again != first {
		t.Fatalf("replay = %+v, replay=%v, err=%v", again, replay, err)
	}
	for _, changed := range []Command{{"OTHER", Receive, 10}, {delivery.SKU, Issue, 10}, {delivery.SKU, Receive, 11}} {
		if _, _, err := store.Move(ctx, "delivery:1", changed); !errors.Is(err, ErrConflict) {
			t.Errorf("reused key for %+v: got %v", changed, err)
		}
	}
	if n, err := store.Balance(ctx, delivery.SKU); err != nil || n != 0 {
		t.Fatalf("final balance = %d, %v", n, err)
	}
}

func TestFailedIssueCanBeRetriedAfterRestock(t *testing.T) {
	store := NewStore()
	ctx := context.Background()
	sale := Command{"BOLT", Issue, 2}
	if _, _, err := store.Move(ctx, "sale", sale); !errors.Is(err, ErrInsufficient) {
		t.Fatalf("issue with no stock: %v", err)
	}
	if _, err := store.Balance(ctx, sale.SKU); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after failed issue: %v", err)
	}
	if _, _, err := store.Move(ctx, "restock", Command{sale.SKU, Receive, 3}); err != nil {
		t.Fatal(err)
	}
	if receipt, replay, err := store.Move(ctx, "sale", sale); err != nil || replay || receipt.Balance != 1 {
		t.Fatalf("retried issue = %+v, replay=%v, err=%v", receipt, replay, err)
	}
}

func TestCommandValidation(t *testing.T) {
	store := NewStore()
	ctx := context.Background()
	valid := Command{"PART_1-A", Receive, maxQuantity}
	for _, sku := range []string{"", "part", "_PART", strings.Repeat("A", 33), "A/2", "КОД"} {
		if _, err := store.Balance(ctx, sku); !errors.Is(err, ErrInvalid) {
			t.Errorf("balance for SKU %q: %v", sku, err)
		}
		if _, _, err := store.Move(ctx, "receipt", Command{sku, Receive, 1}); !errors.Is(err, ErrInvalid) {
			t.Errorf("move for SKU %q: %v", sku, err)
		}
	}
	for _, key := range []string{"", " ", "a\nb", strings.Repeat("x", 129)} {
		if _, _, err := store.Move(ctx, key, valid); !errors.Is(err, ErrInvalid) {
			t.Errorf("key %q: %v", key, err)
		}
	}
	for _, cmd := range []Command{{valid.SKU, "adjust", 2}, {valid.SKU, Issue, 0}, {valid.SKU, Receive, -1}, {valid.SKU, Receive, maxQuantity + 1}} {
		if _, _, err := store.Move(ctx, "receipt", cmd); !errors.Is(err, ErrInvalid) {
			t.Errorf("invalid command %+v: %v", cmd, err)
		}
	}
	if _, _, err := store.Move(ctx, strings.Repeat("k", 128), Command{strings.Repeat("Z", 32), Receive, maxQuantity}); err != nil {
		t.Fatalf("upper limits: %v", err)
	}
}

func TestOverflowAndCancellationLeaveBalanceUntouched(t *testing.T) {
	store := NewStore()
	ctx := context.Background()
	store.balances["BOLT"] = math.MaxInt64 - 1
	if _, _, err := store.Move(ctx, "incoming", Command{"BOLT", Receive, 2}); !errors.Is(err, ErrOverflow) {
		t.Fatalf("overflow: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	cmd := Command{"BOLT", Receive, 1}
	if _, _, err := store.Move(cancelled, "incoming", cmd); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
	if receipt, replay, err := store.Move(ctx, "incoming", cmd); err != nil || replay || receipt.Balance != math.MaxInt64 {
		t.Fatalf("valid retry = %+v, replay=%v, err=%v", receipt, replay, err)
	}
}

func TestConcurrentOrdersForLastUnit(t *testing.T) {
	store := NewStore()
	ctx := context.Background()
	if _, _, err := store.Move(ctx, "delivery", Command{"TEA", Receive, 1}); err != nil {
		t.Fatal(err)
	}
	const buyers = 64
	start := make(chan struct{})
	results := make(chan error, buyers)
	for buyer := 0; buyer < buyers; buyer++ {
		go func(buyer int) {
			<-start
			_, _, err := store.Move(ctx, fmt.Sprintf("sale-%d", buyer), Command{"TEA", Issue, 1})
			results <- err
		}(buyer)
	}
	close(start)
	var sold int
	for i := 0; i < buyers; i++ {
		err := <-results
		if err == nil {
			sold++
		} else if !errors.Is(err, ErrInsufficient) {
			t.Errorf("unexpected issue error: %v", err)
		}
	}
	if sold != 1 {
		t.Fatalf("sold %d units, want 1", sold)
	}
}

func TestConcurrentRetriesApplyOnce(t *testing.T) {
	store := NewStore()
	ctx := context.Background()
	const retries = 32
	start := make(chan struct{})
	results := make(chan bool, retries)
	for i := 0; i < retries; i++ {
		go func() {
			<-start
			receipt, replay, err := store.Move(ctx, "delivery", Command{"TEA", Receive, 4})
			if err != nil || receipt.Balance != 4 {
				t.Errorf("retry = %+v, %v", receipt, err)
			}
			results <- replay
		}()
	}
	close(start)
	var applied int
	for i := 0; i < retries; i++ {
		if !<-results {
			applied++
		}
	}
	if applied != 1 {
		t.Fatalf("applied %d times, want 1", applied)
	}
	if n, err := store.Balance(ctx, "TEA"); err != nil || n != 4 {
		t.Fatalf("balance = %d, %v", n, err)
	}
}
