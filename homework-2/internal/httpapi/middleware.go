package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/mail"
	"strings"
	"sync"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/legacy"
	"github.com/google/uuid"
	"github.com/kolomigor/marketplace-homework2/internal/api"
	"github.com/kolomigor/marketplace-homework2/internal/service"
)

type contextKey int

const actorKey contextKey = 0
const logActorKey contextKey = 1

var registerFormats sync.Once

func currentActor(r *http.Request) service.Actor {
	actor, _ := r.Context().Value(actorKey).(service.Actor)
	return actor
}

func New(svc *service.Service, logger *slog.Logger) (http.Handler, error) {
	registerFormats.Do(func() {
		openapi3.DefineStringFormatCallback("uuid", func(value string) error {
			id, err := uuid.Parse(value)
			if err != nil || !strings.EqualFold(value, id.String()) {
				return errors.New("Ожидается UUID в формате 8-4-4-4-12")
			}
			return nil
		})
		openapi3.DefineStringFormatCallback("date-time", func(value string) error {
			if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
				return errors.New("Ожидается дата RFC 3339 с часовым поясом")
			}
			return nil
		})
		openapi3.DefineStringFormatCallback("email", func(value string) error {
			address, err := mail.ParseAddress(value)
			if err != nil || address.Address != value {
				return errors.New("Некорректный адрес электронной почты")
			}
			return nil
		})
	})
	spec, err := api.GetSwagger()
	if err != nil {
		return nil, err
	}
	spec.Servers = nil // The same contract serves local tests and the container address.
	if err := spec.Validate(context.Background()); err != nil {
		return nil, err
	}
	router, err := legacy.NewRouter(spec)
	if err != nil {
		return nil, err
	}
	generated := api.HandlerWithOptions(&handler{service: svc}, api.StdHTTPServerOptions{
		ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			validationError(w, []any{map[string]any{"field": "parameters", "message": "Некорректный параметр"}})
		},
	})
	validated := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route, pathParams, err := router.FindRoute(r)
		if err != nil {
			validationError(w, []any{map[string]any{"field": "endpoint", "message": "Неизвестный путь или HTTP-метод"}})
			return
		}
		operation := route.Operation.OperationID
		// The generator exports operation IDs in its embedded document.
		if operation != "" {
			operation = strings.ToLower(operation[:1]) + operation[1:]
		}
		if operation != "health" && operation != "register" && operation != "login" && operation != "refresh" {
			parts := strings.Fields(r.Header.Get("Authorization"))
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
				writeError(w, service.Fail(401, "TOKEN_INVALID", "Требуется access token", nil))
				return
			}
			actor, err := svc.Authenticate(r.Context(), parts[1])
			if err != nil {
				writeError(w, err)
				return
			}
			if slot, ok := r.Context().Value(logActorKey).(*service.Actor); ok {
				*slot = actor
			}
			r = r.WithContext(context.WithValue(r.Context(), actorKey, actor))
			if !allowed(operation, actor.Role) {
				writeError(w, service.Fail(403, "ACCESS_DENIED", "Недостаточно прав для операции", nil))
				return
			}
		}
		input := &openapi3filter.RequestValidationInput{Request: r, PathParams: pathParams, Route: route, Options: &openapi3filter.Options{
			MultiError: true, AuthenticationFunc: openapi3filter.NoopAuthenticationFunc,
		}}
		if err := openapi3filter.ValidateRequest(r.Context(), input); err != nil {
			validationError(w, violations(err))
			return
		}
		generated.ServeHTTP(w, r)
	})
	return logging(validated, logger), nil
}

func allowed(operation, role string) bool {
	switch operation {
	case "createProduct", "updateProduct", "archiveProduct", "createPromoCode":
		return role == "SELLER" || role == "ADMIN"
	case "createOrder", "getOrder", "updateOrder", "cancelOrder":
		return role == "USER" || role == "ADMIN"
	case "transitionOrder":
		return role == "ADMIN"
	default:
		return role == "USER" || role == "SELLER" || role == "ADMIN"
	}
}

func validationError(w http.ResponseWriter, fields []any) {
	writeError(w, service.Fail(400, "VALIDATION_ERROR", "Некорректные входные данные", map[string]interface{}{"fields": fields}))
}

func violations(err error) []any {
	var fields []any
	var visit func(error, string)
	visit = func(err error, prefix string) {
		if multi, ok := err.(openapi3.MultiError); ok {
			for _, child := range multi {
				visit(child, prefix)
			}
			return
		}
		var request *openapi3filter.RequestError
		if errors.As(err, &request) {
			if request.Parameter != nil {
				prefix = request.Parameter.Name
			}
			if request.Err != nil {
				visit(request.Err, prefix)
				return
			}
		}
		var schema *openapi3.SchemaError
		if errors.As(err, &schema) {
			field := strings.Join(schema.JSONPointer(), ".")
			if prefix != "" {
				field = prefix
			}
			if field == "" {
				field = "body"
			}
			message := schema.Reason
			if message == "" {
				message = "Нарушение ограничений OpenAPI"
			}
			fields = append(fields, map[string]any{"field": field, "message": message})
			return
		}
		if prefix == "" {
			prefix = "body"
		}
		fields = append(fields, map[string]any{"field": prefix, "message": "Некорректный формат или значение"})
	}
	visit(err, "")
	return fields
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *statusWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(data)
}
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func logging(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		requestID := uuid.NewString()
		w.Header().Set("X-Request-Id", requestID)
		out := &statusWriter{ResponseWriter: w}
		var body any
		actor := new(service.Actor)
		r = r.WithContext(context.WithValue(r.Context(), logActorKey, actor))
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.Error("handler panic", "request_id", requestID, "error", fmt.Sprint(recovered))
				if out.status == 0 {
					writeError(out, errors.New("handler panic"))
				}
			}
			if out.status == 0 {
				out.status = 200
			}
			var actorID any
			if actor.ID != "" {
				actorID = actor.ID
			}
			attrs := []any{"request_id", requestID, "method", r.Method, "endpoint", r.URL.Path, "status_code", out.status, "duration_ms", float64(time.Since(started).Microseconds()) / 1000, "user_id", actorID, "timestamp", started.UTC().Format(time.RFC3339Nano)}
			if r.Method == "POST" || r.Method == "PUT" || r.Method == "DELETE" {
				attrs = append(attrs, "request_body", body)
			}
			logger.Info("api_request", attrs...)
		}()
		if r.Body != nil {
			data, err := io.ReadAll(http.MaxBytesReader(out, r.Body, 1<<20))
			if err != nil {
				validationError(out, []any{map[string]any{"field": "body", "message": "Размер тела запроса превышает 1 MiB"}})
				return
			}
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(data))
			if len(data) > 0 {
				if err := json.Unmarshal(data, &body); err != nil {
					body = "Некорректный JSON"
					validationError(out, []any{map[string]any{"field": "body", "message": "Ожидается один корректный JSON-документ"}})
					return
				} else {
					redact(body)
				}
			}
		}
		next.ServeHTTP(out, r)
	})
}

func redact(value any) {
	switch data := value.(type) {
	case map[string]any:
		for key, child := range data {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "password") || strings.Contains(lower, "token") {
				data[key] = "***"
			} else {
				redact(child)
			}
		}
	case []any:
		for _, child := range data {
			redact(child)
		}
	}
}
