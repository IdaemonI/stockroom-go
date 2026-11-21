package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/IdaemonI/stockroom-go/internal/httpapi"
	"github.com/IdaemonI/stockroom-go/internal/inventory"
)

func perform(handler http.Handler, method, path, contentType, key, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func expectStatus(t *testing.T, rec *httptest.ResponseRecorder, status int) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("got %d, want %d; body %s", rec.Code, status, rec.Body)
	}
}

func TestMovementAndRetry(t *testing.T) {
	api := httpapi.New(inventory.NewStore())
	move := func(key, body string) *httptest.ResponseRecorder {
		return perform(api, "POST", "/v1/movements", "application/json; charset=utf-8", key, body)
	}
	receive := `{"sku":"BEANS","kind":"receive","quantity":7}`
	issue := `{"sku":"BEANS","kind":"issue","quantity":3}`
	first := move("delivery-1", receive)
	expectStatus(t, first, http.StatusCreated)
	var receipt inventory.Receipt
	if err := json.Unmarshal(first.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.SKU != "BEANS" || receipt.Kind != "receive" || receipt.Quantity != 7 || receipt.Balance != 7 || receipt.RequestID != "delivery-1" {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}
	expectStatus(t, move("order-1", issue), http.StatusCreated)
	retry := move("delivery-1", receive)
	expectStatus(t, retry, http.StatusOK)
	if retry.Body.String() != first.Body.String() {
		t.Fatalf("retry changed receipt: %s", retry.Body)
	}
	expectStatus(t, move("delivery-1", issue), http.StatusConflict)
	expectStatus(t, move("order-2", `{"sku":"BEANS","kind":"issue","quantity":9}`), http.StatusConflict)

	stock := perform(api, "GET", "/v1/stock/BEANS", "", "", "")
	expectStatus(t, stock, http.StatusOK)
	var item struct {
		SKU     string `json:"sku"`
		Balance int64  `json:"balance"`
	}
	if err := json.Unmarshal(stock.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	if item.SKU != "BEANS" || item.Balance != 4 {
		t.Fatalf("unexpected stock: %+v", item)
	}
}

func TestRejectedRequests(t *testing.T) {
	command := `{"sku":"MUG","kind":"receive","quantity":1}`
	const mimeJSON = "application/json"
	cases := []struct {
		name, contentType, key, body string
		status                       int
	}{
		{"wrong media type", "text/plain", "x", command, 415},
		{"missing key", mimeJSON, "", command, 400},
		{"invalid key", mimeJSON, "two words", command, 400},
		{"missing quantity", mimeJSON, "x", `{"sku":"MUG","kind":"receive"}`, 400},
		{"unknown field", mimeJSON, "x", `{"sku":"MUG","type":"receive","quantity":1}`, 400},
		{"invalid kind", mimeJSON, "x", `{"sku":"MUG","kind":"ship","quantity":1}`, 400},
		{"trailing object", mimeJSON, "x", command + " {}", 400},
		{"malformed", mimeJSON, "x", `{"sku":`, 400},
		{"oversized", mimeJSON, "x", command + strings.Repeat(" ", 4096), 413},
	}
	api := httpapi.New(inventory.NewStore())
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := perform(api, "POST", "/v1/movements", tc.contentType, tc.key, tc.body)
			expectStatus(t, rec, tc.status)
			var response struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || response.Error == "" {
				t.Fatalf("expected JSON error: %s", rec.Body)
			}
		})
	}
	expectStatus(t, perform(api, "GET", "/v1/stock/MUG", "", "", ""), http.StatusNotFound)
	expectStatus(t, perform(api, "GET", "/v1/stock/lowercase", "", "", ""), http.StatusBadRequest)
	expectStatus(t, perform(api, "GET", "/v1/movements", "", "", ""), http.StatusMethodNotAllowed)
}

func TestHealth(t *testing.T) {
	api := httpapi.New(inventory.NewStore())
	rec := perform(api, "GET", "/healthz", "", "", "")
	expectStatus(t, rec, http.StatusOK)
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("unexpected content type: %s", rec.Header().Get("Content-Type"))
	}
	var result map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || result["status"] != "ok" {
		t.Fatalf("unexpected health response: %s", rec.Body)
	}
}
