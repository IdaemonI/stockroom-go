package httpapi

import (
	"encoding/base64"
	"github.com/IdaemonI/stockroom-go/internal/inventory"
	"net/http"
	"strconv"
	"strings"
)

func history(store Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sku := r.PathValue("sku")
		params := r.URL.Query()
		limit := 20
		if params.Has("limit") {
			n, err := strconv.Atoi(params.Get("limit"))
			if err != nil || n < 1 || n > 100 || len(params["limit"]) != 1 {
				fail(w, 400, "limit must be between 1 and 100")
				return
			}
			limit = n
		}
		var before int64
		if params.Has("cursor") {
			raw, err := base64.RawURLEncoding.DecodeString(params.Get("cursor"))
			owner, id, ok := strings.Cut(string(raw), ":")
			n, parseErr := strconv.ParseInt(id, 10, 64)
			if err != nil || !ok || owner != sku || parseErr != nil || n <= 0 || len(params["cursor"]) != 1 {
				fail(w, 400, "invalid cursor for SKU")
				return
			}
			before = n
		}
		rows, err := store.History(r.Context(), sku, before, limit+1)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		page := struct {
			Movements []inventory.Movement `json:"movements"`
			Next      string               `json:"next_cursor,omitempty"`
		}{Movements: rows}
		if len(rows) > limit {
			page.Movements = rows[:limit]
			page.Next = base64.RawURLEncoding.EncodeToString([]byte(sku + ":" + strconv.FormatInt(rows[limit-1].ID, 10)))
		}
		writeJSON(w, http.StatusOK, page)
	}
}
