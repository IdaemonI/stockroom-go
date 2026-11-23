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

func newAPI(t *testing.T) http.Handler {
	t.Helper()
	keys, err := httpapi.NewKeys("reader-test", "writer-test")
	if err != nil {
		t.Fatal(err)
	}
	return httpapi.New(inventory.NewStore(), keys)
}

func perform(handler http.Handler, method, path, contentType, key, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer writer-test")
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
	api := newAPI(t)
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
	api := newAPI(t)
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
	api := newAPI(t)
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

func TestInventoryRolesAndHistory(t *testing.T) {
	api := newAPI(t)
	call := func(method, path, auth, key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(`{"sku":"MUG","kind":"receive","quantity":1}`))
		r.Header.Set("Content-Type", "application/json")
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	const history = "/v1/stock/MUG/movements"
	for _, auth := range []string{"", "Bearer unknown", "Basic writer-test"} {
		for _, path := range []string{history, "/v1/stock/MUG"} {
			expectStatus(t, call("GET", path, auth, ""), 401)
		}
		expectStatus(t, call("POST", "/v1/movements", auth, "bad"), 401)
	}
	const writer = "Bearer writer-test"
	const reader = "Bearer reader-test"
	expectStatus(t, call("GET", "/healthz", "", ""), 200)
	expectStatus(t, call("POST", "/v1/movements", reader, "bad"), 403)
	for _, key := range []string{"one", "two", "three"} {
		expectStatus(t, call("POST", "/v1/movements", writer, key), 201)
	}
	expectStatus(t, call("POST", "/v1/movements", writer, "two"), 200)
	for _, role := range []string{reader, writer} {
		expectStatus(t, call("GET", "/v1/stock/MUG", role, ""), 200)
	}
	page := func(path, role string) ([]inventory.Movement, string) {
		rec := call("GET", path, role, "")
		expectStatus(t, rec, 200)
		var p struct {
			Movements []inventory.Movement `json:"movements"`
			Next      string               `json:"next_cursor"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		return p.Movements, p.Next
	}
	rows, cursor := page(history+"?limit=2", reader)
	if len(rows) != 2 || rows[0].RequestID != "three" || rows[1].RequestID != "two" || cursor == "" {
		t.Fatalf("page one: %+v %q", rows, cursor)
	}
	expectStatus(t, call("POST", "/v1/movements", writer, "newer"), 201)
	rows, next := page(history+"?limit=2&cursor="+cursor, writer)
	if len(rows) != 1 || rows[0].RequestID != "one" || next != "" {
		t.Fatalf("page two: %+v %q", rows, next)
	}
	for _, path := range []string{history + "?limit=101", history + "?cursor=broken", "/v1/stock/ELSE/movements?cursor=" + cursor} {
		expectStatus(t, call("GET", path, reader, ""), 400)
	}
	for _, pair := range [][2]string{{"", "write"}, {"read", ""}, {"same", "same"}} {
		if _, err := httpapi.NewKeys(pair[0], pair[1]); err == nil {
			t.Fatal("accepted invalid configuration")
		}
	}
}
