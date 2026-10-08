package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/legacy"
	"github.com/google/uuid"
	"github.com/kolomigor/marketplace-homework2/internal/api"
	"github.com/kolomigor/marketplace-homework2/internal/service"
	"github.com/shopspring/decimal"
)

func init() { decimal.MarshalJSONWithoutQuotes = true }

func TestOpenAPI(t *testing.T) {
	spec := testSpec(t)
	if err := spec.Validate(context.Background()); err != nil {
		t.Fatalf("contract invalid: %v", err)
	}
}

// A nil DB makes any request that slips past contract validation fail with 500.
// Invalid bodies must return useful 400 errors without invoking persistence.
func TestInvalidAuthRejectedBeforeService(t *testing.T) {
	cases := []struct {
		name, path, body, field, secret string
	}{
		{"malformed JSON", "/auth/register", `{"email":"user@example.com","password":"secret-malformed",`, "body", "secret-malformed"},
		{"two JSON documents", "/auth/register", `{"email":"user@example.com","password":"secret-trailing-json"} {}`, "body", "secret-trailing-json"},
		{"short password", "/auth/register", `{"email":"user@example.com","password":"sh0rt!"}`, "password", "sh0rt!"},
		{"missing email", "/auth/register", `{"password":"secret-required-email"}`, "email", "secret-required-email"},
		{"invalid email", "/auth/register", `{"email":"this-is-not-an-email","password":"secret-email-value"}`, "email", "secret-email-value"},
		{"public admin registration", "/auth/register", `{"email":"user@example.com","password":"secret-role-value","role":"ADMIN"}`, "role", "secret-role-value"},
		{"unknown property", "/auth/register", `{"email":"user@example.com","password":"secret-property-value","extra":true}`, "body", "secret-property-value"},
		{"empty refresh", "/auth/refresh", `{"refresh_token":""}`, "refresh_token", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			h, err := New(&service.Service{}, slog.New(slog.NewJSONHandler(&logs, nil)))
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			out := httptest.NewRecorder()
			h.ServeHTTP(out, r)
			if out.Code != 400 {
				t.Fatalf("status=%d body=%s logs=%s", out.Code, out.Body.String(), logs.String())
			}
			var body api.ErrorResponse
			if err := json.Unmarshal(out.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.ErrorCode != api.VALIDATIONERROR || body.Details == nil {
				t.Fatalf("unexpected error %#v", body)
			}
			fields, ok := (*body.Details)["fields"].([]any)
			if !ok || len(fields) == 0 {
				t.Fatalf("missing field violations: %#v", body.Details)
			}
			found := false
			for _, raw := range fields {
				field, ok := raw.(map[string]any)
				if !ok || field["field"] == "" || field["message"] == "" {
					t.Fatalf("invalid violation: %#v", raw)
				}
				if field["field"] == tc.field {
					found = true
				}
			}
			if !found {
				t.Fatalf("violation for %s absent: %#v", tc.field, fields)
			}
			if tc.secret != "" && (strings.Contains(out.Body.String(), tc.secret) || strings.Contains(logs.String(), tc.secret)) {
				t.Fatal("password value leaked in error or logs")
			}
			entry := readRequestLog(t, logs.Bytes())
			assertRequestLog(t, entry, out, http.MethodPost, tc.path)
		})
	}
}

func TestLoggingEveryRequestAndRedactingSecrets(t *testing.T) {
	cases := []struct{ method, body string }{
		{http.MethodGet, ""},
		{http.MethodPost, `{"password":"private-password","nested":[{"refresh_token":"private-refresh"}],"name":"visible"}`},
		{http.MethodPut, `{"old_password":"private-password","access_token":"private-access"}`},
		{http.MethodDelete, `{"password":"private-password"}`},
		{http.MethodPost, `{"password":"private-malformed",`},
	}
	for _, tc := range cases {
		t.Run(tc.method+fmt.Sprint(len(tc.body)), func(t *testing.T) {
			var logs bytes.Buffer
			actorID := uuid.NewString()
			h := logging(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPut {
					*r.Context().Value(logActorKey).(*service.Actor) = service.Actor{ID: actorID, Role: "USER"}
				}
				data, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				if string(data) != tc.body {
					t.Fatal("logging changed the request body used by validation")
				}
				w.WriteHeader(http.StatusAccepted)
			}), slog.New(slog.NewJSONHandler(&logs, nil)))
			r := httptest.NewRequest(tc.method, "/log-check", strings.NewReader(tc.body))
			r.Header.Set("X-Request-Id", "untrusted-client-value")
			out := httptest.NewRecorder()
			h.ServeHTTP(out, r)
			entry := readRequestLog(t, logs.Bytes())
			assertRequestLog(t, entry, out, tc.method, "/log-check")
			if out.Header().Get("X-Request-Id") == "untrusted-client-value" {
				t.Fatal("request UUID was not generated by middleware")
			}
			if tc.method == http.MethodPut {
				if entry["user_id"] != actorID {
					t.Fatalf("authorized actor absent in logs: %#v", entry)
				}
			} else if entry["user_id"] != nil {
				t.Fatalf("anonymous user must be null: %#v", entry)
			}
			if tc.method == http.MethodGet {
				if _, ok := entry["request_body"]; ok {
					t.Fatal("GET body should not be logged")
				}
			} else if _, ok := entry["request_body"]; !ok {
				t.Fatal("mutating request body not logged")
			}
			for _, secret := range []string{"private-password", "private-refresh", "private-access", "private-malformed"} {
				if strings.Contains(logs.String(), secret) {
					t.Fatalf("secret %s leaked in logs", secret)
				}
			}
			if tc.method == http.MethodPost && strings.Contains(tc.body, "visible") {
				body := entry["request_body"].(map[string]any)
				if body["name"] != "visible" || body["password"] != "***" {
					t.Fatalf("redaction removed non-sensitive data: %#v", body)
				}
			}
		})
	}
}

func TestOpenAPIRequestAndResponseBoundaries(t *testing.T) {
	spec := testSpec(t)
	router, err := legacy.NewRouter(spec)
	if err != nil {
		t.Fatal(err)
	}
	const productID = "a8eeaa1a-8f01-49b0-91da-19762c3a7502"
	product := func(price string) string {
		return fmt.Sprintf(`{"name":"Keyboard","price":%s,"stock":0,"category":"electronics","status":"ACTIVE"}`, price)
	}
	items := func(n, quantity int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = fmt.Sprintf(`{"product_id":%q,"quantity":%d}`, uuid.NewString(), quantity)
		}
		return `{"items":[` + strings.Join(parts, ",") + `]}`
	}
	cases := []struct {
		name, method, path, body string
		valid                    bool
	}{
		{"price 0.01", "POST", "/products", product("0.01"), true},
		{"price 4.99", "POST", "/products", product("4.99"), true},
		{"price DB maximum", "POST", "/products", product("9999999999.99"), true},
		{"zero price", "POST", "/products", product("0"), false},
		{"negative price", "POST", "/products", product("-1"), false},
		{"subcent price", "POST", "/products", product("0.015"), false},
		{"price overflow", "POST", "/products", product("10000000000"), false},
		{"unknown status", "POST", "/products", strings.ReplaceAll(product("4.99"), "ACTIVE", "DELETED"), false},
		{"negative stock", "POST", "/products", strings.ReplaceAll(product("4.99"), `"stock":0`, `"stock":-1`), false},
		{"missing name", "POST", "/products", `{"price":4.99,"stock":0,"category":"test","status":"ACTIVE"}`, false},
		{"unknown property", "POST", "/products", strings.TrimSuffix(product("4.99"), "}") + `,"extra":true}`, false},
		{"1 item 1 unit", "POST", "/orders", items(1, 1), true},
		{"50 items 999 units", "POST", "/orders", items(50, 999), true},
		{"no items", "POST", "/orders", items(0, 1), false},
		{"51 items", "POST", "/orders", items(51, 1), false},
		{"zero quantity", "POST", "/orders", items(1, 0), false},
		{"1000 quantity", "POST", "/orders", items(1, 1000), false},
		{"invalid item UUID", "POST", "/orders", `{"items":[{"product_id":"invalid-id","quantity":1}]}`, false},
		{"valid promo pattern", "POST", "/orders", strings.TrimSuffix(items(1, 1), "}") + `,"promo_code":"SAVE_10"}`, true},
		{"invalid promo pattern", "POST", "/orders", strings.TrimSuffix(items(1, 1), "}") + `,"promo_code":"save10"}`, false},
		{"short promo pattern", "POST", "/orders", strings.TrimSuffix(items(1, 1), "}") + `,"promo_code":"ABC"}`, false},
		{"invalid email format", "POST", "/auth/register", `{"email":"not-an-email","password":"password123"}`, false},
		{"valid product UUID", "GET", "/products/" + productID, "", true},
		{"invalid product UUID", "GET", "/products/invalid-uuid", "", false},
		{"URN UUID is not canonical", "GET", "/products/urn:uuid:" + productID, "", false},
		{"UUID without separators", "GET", "/products/" + strings.ReplaceAll(productID, "-", ""), "", false},
		{"default pagination", "GET", "/products", "", true},
		{"zero page size", "GET", "/products?size=0", "", false},
		{"negative page", "GET", "/products?page=-1", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			if tc.body != "" {
				r.Header.Set("Content-Type", "application/json")
			}
			route, params, err := router.FindRoute(r)
			if err != nil {
				t.Fatal(err)
			}
			input := &openapi3filter.RequestValidationInput{Request: r, Route: route, PathParams: params, Options: &openapi3filter.Options{MultiError: true, AuthenticationFunc: openapi3filter.NoopAuthenticationFunc}}
			err = openapi3filter.ValidateRequest(r.Context(), input)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v expected=%v error=%v", err == nil, tc.valid, err)
			}
		})
	}

	// Generated decimal DTOs must serialize as JSON numbers and remain valid responses.
	for _, amount := range []string{"0.01", "4.99", "9999999999.99"} {
		t.Run("response decimal "+amount, func(t *testing.T) {
			value := api.ProductResponse{Id: productID, Name: "Keyboard", Price: decimal.RequireFromString(amount), Stock: 1, Category: "test", Status: api.ACTIVE, SellerId: uuid.NewString(), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
			data, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var decoded map[string]any
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatal(err)
			}
			if _, ok := decoded["price"].(float64); !ok {
				t.Fatalf("price must be a JSON number: %s", data)
			}
			r := httptest.NewRequest("GET", "/products/"+productID, nil)
			route, params, err := router.FindRoute(r)
			if err != nil {
				t.Fatal(err)
			}
			input := &openapi3filter.ResponseValidationInput{RequestValidationInput: &openapi3filter.RequestValidationInput{Request: r, Route: route, PathParams: params}, Status: 200, Header: http.Header{"Content-Type": []string{"application/json"}, "X-Request-Id": []string{uuid.NewString()}}}
			input.SetBodyBytes(data)
			if err := openapi3filter.ValidateResponse(r.Context(), input); err != nil {
				t.Fatalf("generated response violates contract: %v", err)
			}
		})
	}
}

func testSpec(t *testing.T) *openapi3.T {
	t.Helper()
	// Production initialization registers its format callbacks exactly once.
	if _, err := New(&service.Service{}, slog.New(slog.NewJSONHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	spec, err := api.GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	spec.Servers = nil
	return spec
}

func readRequestLog(t *testing.T, data []byte) map[string]any {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	if len(lines) != 1 {
		t.Fatalf("expected one request log, got %s", data)
	}
	var entry map[string]any
	if err := json.Unmarshal(lines[0], &entry); err != nil {
		t.Fatalf("request log is not JSON: %v", err)
	}
	if entry["msg"] != "api_request" {
		t.Fatalf("unexpected log: %#v", entry)
	}
	return entry
}

func assertRequestLog(t *testing.T, entry map[string]any, out *httptest.ResponseRecorder, method, path string) {
	t.Helper()
	requestID := out.Header().Get("X-Request-Id")
	if _, err := uuid.Parse(requestID); err != nil {
		t.Fatalf("response request UUID invalid: %q", requestID)
	}
	if entry["request_id"] != requestID || entry["method"] != method || entry["endpoint"] != path || entry["status_code"] != float64(out.Code) {
		t.Fatalf("log does not match response: %#v", entry)
	}
	if duration, ok := entry["duration_ms"].(float64); !ok || duration < 0 {
		t.Fatalf("invalid duration: %#v", entry["duration_ms"])
	}
	if _, err := time.Parse(time.RFC3339Nano, fmt.Sprint(entry["timestamp"])); err != nil {
		t.Fatalf("invalid timestamp: %#v", entry["timestamp"])
	}
	if _, ok := entry["user_id"]; !ok {
		t.Fatal("user_id missing")
	}
}
