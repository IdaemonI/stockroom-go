// Package inventory applies stock movements for a single warehouse.
package inventory

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sync"
)

const (
	Receive = "receive"
	Issue   = "issue"

	maxQuantity = 1_000_000
)

var (
	ErrInvalid      = errors.New("invalid input")
	ErrNotFound     = errors.New("SKU not found")
	ErrConflict     = errors.New("idempotency key already used for a different movement")
	ErrInsufficient = errors.New("insufficient stock")
	ErrOverflow     = errors.New("balance limit exceeded")

	skuPattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,31}$`)
	keyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
)

type Command struct {
	SKU      string `json:"sku"`
	Kind     string `json:"kind"`
	Quantity int64  `json:"quantity"`
}

type Receipt struct {
	RequestID string `json:"request_id"`
	SKU       string `json:"sku"`
	Kind      string `json:"kind"`
	Quantity  int64  `json:"quantity"`
	Balance   int64  `json:"balance"`
}

type Store struct {
	mu       sync.RWMutex
	balances map[string]int64
	receipts map[string]Receipt
}

func NewStore() *Store {
	return &Store{
		balances: make(map[string]int64),
		receipts: make(map[string]Receipt),
	}
}

func validateCommand(key string, cmd Command) error {
	switch {
	case !keyPattern.MatchString(key):
		return fmt.Errorf("%w: invalid idempotency key", ErrInvalid)
	case !skuPattern.MatchString(cmd.SKU):
		return fmt.Errorf("%w: invalid SKU", ErrInvalid)
	case cmd.Kind != Receive && cmd.Kind != Issue:
		return fmt.Errorf("%w: kind must be receive or issue", ErrInvalid)
	case cmd.Quantity < 1 || cmd.Quantity > maxQuantity:
		return fmt.Errorf("%w: quantity must be between 1 and %d", ErrInvalid, maxQuantity)
	}
	return nil
}

// Move applies a movement once. A replay returns the original receipt and true,
// even when later movements have changed the balance.
func (s *Store) Move(ctx context.Context, key string, cmd Command) (Receipt, bool, error) {
	if err := validateCommand(key, cmd); err != nil {
		return Receipt{}, false, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Receipt{}, false, err
	}
	if prior, ok := s.receipts[key]; ok {
		if prior.SKU != cmd.SKU || prior.Kind != cmd.Kind || prior.Quantity != cmd.Quantity {
			return Receipt{}, false, ErrConflict
		}
		return prior, true, nil
	}

	balance := s.balances[cmd.SKU]
	switch cmd.Kind {
	case Issue:
		if balance < cmd.Quantity {
			return Receipt{}, false, ErrInsufficient
		}
		balance -= cmd.Quantity
	case Receive:
		if balance > math.MaxInt64-cmd.Quantity {
			return Receipt{}, false, ErrOverflow
		}
		balance += cmd.Quantity
	}

	receipt := Receipt{RequestID: key, SKU: cmd.SKU, Kind: cmd.Kind, Quantity: cmd.Quantity, Balance: balance}
	s.balances[cmd.SKU] = balance
	s.receipts[key] = receipt
	return receipt, false, nil
}

func (s *Store) Balance(ctx context.Context, sku string) (int64, error) {
	if !skuPattern.MatchString(sku) {
		return 0, fmt.Errorf("%w: invalid SKU", ErrInvalid)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	balance, ok := s.balances[sku]
	if !ok {
		return 0, ErrNotFound
	}
	return balance, nil
}
