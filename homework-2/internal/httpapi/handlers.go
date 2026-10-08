package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/kolomigor/marketplace-homework2/internal/api"
	"github.com/kolomigor/marketplace-homework2/internal/service"
)

type handler struct{ service *service.Service }

var _ api.ServerInterface = (*handler)(nil)

func respond(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if status != http.StatusNoContent {
		_ = json.NewEncoder(w).Encode(body)
	}
}

func writeError(w http.ResponseWriter, err error) {
	var app *service.AppError
	if !errors.As(err, &app) {
		app = &service.AppError{Status: 500, Code: "INTERNAL_ERROR", Message: "Внутренняя ошибка сервера"}
	}
	body := api.ErrorResponse{ErrorCode: api.ErrorResponseErrorCode(app.Code), Message: app.Message}
	if app.Details != nil {
		body.Details = &app.Details
	}
	respond(w, app.Status, body)
}

func decode[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	var input T
	decoder := json.NewDecoder(r.Body)
	err := decoder.Decode(&input)
	if err == nil {
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			err = errors.New("multiple JSON values")
		}
	}
	if err != nil {
		writeError(w, service.Fail(400, "VALIDATION_ERROR", "Некорректное тело запроса", map[string]interface{}{"fields": []any{map[string]any{"field": "body", "message": "Ожидается корректный JSON"}}}))
		return input, false
	}
	return input, true
}

func finish[T any](w http.ResponseWriter, status int, value T, err error) {
	if err != nil {
		writeError(w, err)
		return
	}
	respond(w, status, value)
}

func (h *handler) Health(w http.ResponseWriter, r *http.Request) {
	if err := h.service.DB.Ping(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	respond(w, 200, api.HealthResponse{Status: api.Ok})
}

func (h *handler) Register(w http.ResponseWriter, r *http.Request) {
	in, ok := decode[api.RegisterRequest](w, r)
	if !ok {
		return
	}
	value, err := h.service.Register(r.Context(), in)
	finish(w, 201, value, err)
}

func (h *handler) Login(w http.ResponseWriter, r *http.Request) {
	in, ok := decode[api.LoginRequest](w, r)
	if !ok {
		return
	}
	value, err := h.service.Login(r.Context(), in)
	finish(w, 200, value, err)
}

func (h *handler) Refresh(w http.ResponseWriter, r *http.Request) {
	in, ok := decode[api.RefreshRequest](w, r)
	if !ok {
		return
	}
	value, err := h.service.Refresh(r.Context(), in)
	finish(w, 200, value, err)
}

func (h *handler) CreateProduct(w http.ResponseWriter, r *http.Request) {
	in, ok := decode[api.ProductCreate](w, r)
	if !ok {
		return
	}
	value, err := h.service.CreateProduct(r.Context(), currentActor(r), in)
	if err == nil {
		w.Header().Set("Location", "/products/"+value.Id)
	}
	finish(w, 201, value, err)
}

func (h *handler) GetProduct(w http.ResponseWriter, r *http.Request, id string) {
	value, err := h.service.GetProduct(r.Context(), currentActor(r), id)
	finish(w, 200, value, err)
}

func (h *handler) ListProducts(w http.ResponseWriter, r *http.Request, params api.ListProductsParams) {
	value, err := h.service.ListProducts(r.Context(), currentActor(r), params)
	finish(w, 200, value, err)
}

func (h *handler) UpdateProduct(w http.ResponseWriter, r *http.Request, id string) {
	in, ok := decode[api.ProductUpdate](w, r)
	if !ok {
		return
	}
	value, err := h.service.UpdateProduct(r.Context(), currentActor(r), id, in)
	finish(w, 200, value, err)
}

func (h *handler) ArchiveProduct(w http.ResponseWriter, r *http.Request, id string) {
	if err := h.service.ArchiveProduct(r.Context(), currentActor(r), id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(204)
}

func (h *handler) CreateOrder(w http.ResponseWriter, r *http.Request) {
	in, ok := decode[api.OrderCreate](w, r)
	if !ok {
		return
	}
	value, err := h.service.CreateOrder(r.Context(), currentActor(r), in)
	if err == nil {
		w.Header().Set("Location", "/orders/"+value.Id)
	}
	finish(w, 201, value, err)
}

func (h *handler) GetOrder(w http.ResponseWriter, r *http.Request, id string) {
	value, err := h.service.GetOrder(r.Context(), currentActor(r), id)
	finish(w, 200, value, err)
}

func (h *handler) UpdateOrder(w http.ResponseWriter, r *http.Request, id string) {
	in, ok := decode[api.OrderUpdate](w, r)
	if !ok {
		return
	}
	value, err := h.service.UpdateOrder(r.Context(), currentActor(r), id, in)
	finish(w, 200, value, err)
}

func (h *handler) CancelOrder(w http.ResponseWriter, r *http.Request, id string) {
	value, err := h.service.CancelOrder(r.Context(), currentActor(r), id)
	finish(w, 200, value, err)
}

func (h *handler) TransitionOrder(w http.ResponseWriter, r *http.Request, id string) {
	in, ok := decode[api.OrderStatusUpdate](w, r)
	if !ok {
		return
	}
	value, err := h.service.TransitionOrder(r.Context(), currentActor(r), id, api.OrderStatus(in.Status))
	finish(w, 200, value, err)
}

func (h *handler) CreatePromoCode(w http.ResponseWriter, r *http.Request) {
	in, ok := decode[api.PromoCodeCreate](w, r)
	if !ok {
		return
	}
	value, err := h.service.CreatePromoCode(r.Context(), currentActor(r), in)
	finish(w, 201, value, err)
}
