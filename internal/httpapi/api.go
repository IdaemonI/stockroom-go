package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/IdaemonI/stockroom-go/internal/inventory"
)

type Store interface {
	History(context.Context, string, int64, int) ([]inventory.Movement, error)
	Move(context.Context, string, inventory.Command) (inventory.Receipt, bool, error)
	Balance(context.Context, string) (int64, error)
}

const maxBody = 4096

func New(store Store, keys Keys) http.Handler {
	mux := http.NewServeMux()
	root := http.NewServeMux()
	root.Handle("/v1/", keys.protect(mux))
	mux.HandleFunc("GET /v1/stock/{sku}/movements", history(store))
	root.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /v1/stock/{sku}", func(w http.ResponseWriter, r *http.Request) {
		sku := r.PathValue("sku")
		balance, err := store.Balance(r.Context(), sku)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, struct {
			SKU     string `json:"sku"`
			Balance int64  `json:"balance"`
		}{sku, balance})
	})
	mux.HandleFunc("POST /v1/movements", func(w http.ResponseWriter, r *http.Request) {
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			fail(w, http.StatusUnsupportedMediaType, "use Content-Type application/json")
			return
		}
		keys := r.Header.Values("Idempotency-Key")
		if len(keys) != 1 || keys[0] == "" {
			fail(w, http.StatusBadRequest, "provide one Idempotency-Key header")
			return
		}

		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				fail(w, http.StatusRequestEntityTooLarge, "body exceeds 4096 bytes")
			} else {
				fail(w, http.StatusBadRequest, "could not read request body")
			}
			return
		}
		var command inventory.Command
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&command); err != nil || decoder.Decode(new(any)) != io.EOF {
			fail(w, http.StatusBadRequest, "expected one JSON object with sku, kind and quantity")
			return
		}
		receipt, replay, err := store.Move(r.Context(), keys[0], command)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		status := http.StatusCreated
		if replay {
			status = http.StatusOK
		}
		writeJSON(w, status, receipt)
	})
	return root
}

func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, inventory.ErrInvalid):
		fail(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, inventory.ErrNotFound):
		fail(w, http.StatusNotFound, "stock item not found")
	case errors.Is(err, inventory.ErrConflict):
		fail(w, http.StatusConflict, "idempotency key already used for another movement")
	case errors.Is(err, inventory.ErrInsufficient):
		fail(w, http.StatusConflict, "insufficient stock")
	case errors.Is(err, inventory.ErrOverflow):
		fail(w, http.StatusConflict, "stock balance would overflow")
	default:
		fail(w, http.StatusInternalServerError, "internal server error")
	}
}

func fail(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
