package inventory

import (
	"context"
	"fmt"
	"sort"
	"time"
)

type Movement struct {
	Receipt
	ID        int64     `json:"id"`
	CreatedAt time.Time `json:"created_at"`
}

// History returns up to limit records in descending ledger order, before the
// exclusive ID when supplied. Zero starts at the newest committed movement.
func (s *Store) History(ctx context.Context, sku string, before int64, limit int) ([]Movement, error) {
	if !skuPattern.MatchString(sku) {
		return nil, fmt.Errorf("%w: invalid SKU", ErrInvalid)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ledger := s.history[sku]
	end := len(ledger)
	if before > 0 {
		end = sort.Search(end, func(i int) bool { return ledger[i].ID >= before })
	}
	result := make([]Movement, 0, min(limit, end))
	for i := end - 1; i >= 0 && len(result) < limit; i-- {
		result = append(result, ledger[i])
	}
	return result, nil
}

func (s *Postgres) History(ctx context.Context, sku string, before int64, limit int) ([]Movement, error) {
	if !skuPattern.MatchString(sku) {
		return nil, fmt.Errorf("%w: invalid SKU", ErrInvalid)
	}
	const fields = "SELECT id, created_at, request_id, sku, kind, quantity, balance FROM stock_movements WHERE sku = $1"
	query := fields + " ORDER BY id DESC LIMIT $2"
	args := []any{sku, limit}
	if before > 0 {
		query = fields + " AND id < $3 ORDER BY id DESC LIMIT $2"
		args = append(args, before)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Movement, 0)
	for rows.Next() {
		var m Movement
		if err := rows.Scan(&m.ID, &m.CreatedAt, &m.RequestID, &m.SKU, &m.Kind, &m.Quantity, &m.Balance); err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, rows.Err()
}
