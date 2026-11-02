package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/kolomigor/marketplace-homework2/internal/api"
)

const productColumns = `id::text, seller_id::text, name, description, price::text, stock, category, status, created_at, updated_at`

type scanner interface {
	Scan(...interface{}) error
}

func scanProduct(row scanner) (api.ProductResponse, error) {
	var product api.ProductResponse
	var price, status string
	err := row.Scan(&product.Id, &product.SellerId, &product.Name, &product.Description, &price, &product.Stock,
		&product.Category, &status, &product.CreatedAt, &product.UpdatedAt)
	if err != nil {
		return api.ProductResponse{}, err
	}
	product.Status = api.ProductStatus(status)
	product.Price, err = decimal.NewFromString(price)
	if err != nil {
		return api.ProductResponse{}, fmt.Errorf("parse product price: %w", err)
	}
	return product, nil
}

func productNotFound(id string) error {
	return Fail(http.StatusNotFound, "PRODUCT_NOT_FOUND", "Product not found", map[string]interface{}{"product_id": id})
}

func canManageProduct(actor Actor, sellerID string) bool {
	return actor.Role == "ADMIN" || (actor.Role == "SELLER" && actor.ID == sellerID)
}

func productAccessDenied() error {
	return Fail(http.StatusForbidden, "ACCESS_DENIED", "Only the product owner or an administrator may change the product", nil)
}

func (s *Service) CreateProduct(ctx context.Context, actor Actor, input api.ProductCreate) (api.ProductResponse, error) {
	if !canManageProduct(actor, actor.ID) {
		return api.ProductResponse{}, productAccessDenied()
	}
	product, err := scanProduct(s.DB.QueryRow(ctx, `INSERT INTO products (id, seller_id, name, description, price, stock, category, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING `+productColumns,
		uuid.NewString(), actor.ID, input.Name, input.Description, input.Price.StringFixed(2), input.Stock, input.Category, string(input.Status)))
	if err != nil {
		return api.ProductResponse{}, fmt.Errorf("create product: %w", err)
	}
	return product, nil
}

func (s *Service) GetProduct(ctx context.Context, _ Actor, id string) (api.ProductResponse, error) {
	product, err := scanProduct(s.DB.QueryRow(ctx, `SELECT `+productColumns+` FROM products WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return api.ProductResponse{}, productNotFound(id)
	}
	if err != nil {
		return api.ProductResponse{}, fmt.Errorf("get product: %w", err)
	}
	return product, nil
}

func (s *Service) ListProducts(ctx context.Context, _ Actor, params api.ListProductsParams) (api.ProductPage, error) {
	page, size := 0, 20
	if params.Page != nil {
		page = *params.Page
	}
	if params.Size != nil {
		size = *params.Size
	}
	var status, category *string
	if params.Status != nil {
		value := string(*params.Status)
		status = &value
	}
	if params.Category != nil {
		category = params.Category
	}
	result := api.ProductPage{Items: []api.ProductResponse{}, Page: page, Size: size}
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return api.ProductPage{}, fmt.Errorf("begin product list: %w", err)
	}
	defer tx.Rollback(ctx)
	filter := ` WHERE ($1::product_status IS NULL OR status = $1::product_status) AND ($2::text IS NULL OR category = $2)`
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM products`+filter, status, category).Scan(&result.TotalElements); err != nil {
		return api.ProductPage{}, fmt.Errorf("count products: %w", err)
	}
	rows, err := tx.Query(ctx, `SELECT `+productColumns+` FROM products`+filter+` ORDER BY created_at DESC, id ASC LIMIT $3 OFFSET $4`, status, category, size, int64(page)*int64(size))
	if err != nil {
		return api.ProductPage{}, fmt.Errorf("list products: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		product, err := scanProduct(rows)
		if err != nil {
			return api.ProductPage{}, fmt.Errorf("read product list: %w", err)
		}
		result.Items = append(result.Items, product)
	}
	if err := rows.Err(); err != nil {
		return api.ProductPage{}, fmt.Errorf("read product rows: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return api.ProductPage{}, fmt.Errorf("commit product list: %w", err)
	}
	return result, nil
}

func (s *Service) UpdateProduct(ctx context.Context, actor Actor, id string, input api.ProductUpdate) (api.ProductResponse, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return api.ProductResponse{}, fmt.Errorf("begin product update: %w", err)
	}
	defer tx.Rollback(ctx)
	var sellerID string
	err = tx.QueryRow(ctx, `SELECT seller_id::text FROM products WHERE id = $1 FOR UPDATE`, id).Scan(&sellerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return api.ProductResponse{}, productNotFound(id)
	}
	if err != nil {
		return api.ProductResponse{}, fmt.Errorf("lock product: %w", err)
	}
	if !canManageProduct(actor, sellerID) {
		return api.ProductResponse{}, productAccessDenied()
	}
	var reserved int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(items.quantity), 0)
		FROM order_items items JOIN orders ON orders.id = items.order_id
		WHERE items.product_id = $1 AND orders.status IN ('CREATED', 'PAYMENT_PENDING')`, id).Scan(&reserved); err != nil {
		return api.ProductResponse{}, fmt.Errorf("read product reservations: %w", err)
	}
	// Cancellable orders must always fit back into the available stock column.
	// The product lock serializes inventory changes; no order locks are needed.
	if int64(input.Stock) > int64(math.MaxInt32)-reserved {
		return api.ProductResponse{}, Fail(http.StatusBadRequest, "VALIDATION_ERROR", "Stock cannot accommodate reserved items", map[string]interface{}{
			"fields": []map[string]interface{}{{"field": "stock", "message": "Available stock plus cancellable reservations must not exceed 2147483647", "max_available_stock": int64(math.MaxInt32) - reserved}},
		})
	}
	product, err := scanProduct(tx.QueryRow(ctx, `UPDATE products SET name = $2, description = $3, price = $4,
		stock = $5, category = $6, status = $7 WHERE id = $1 RETURNING `+productColumns,
		id, input.Name, input.Description, input.Price.StringFixed(2), input.Stock, input.Category, string(input.Status)))
	if err != nil {
		return api.ProductResponse{}, fmt.Errorf("update product: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return api.ProductResponse{}, fmt.Errorf("commit product update: %w", err)
	}
	return product, nil
}

func (s *Service) ArchiveProduct(ctx context.Context, actor Actor, id string) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin product archive: %w", err)
	}
	defer tx.Rollback(ctx)
	var sellerID, status string
	err = tx.QueryRow(ctx, `SELECT seller_id::text, status FROM products WHERE id = $1 FOR UPDATE`, id).Scan(&sellerID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return productNotFound(id)
	}
	if err != nil {
		return fmt.Errorf("lock product: %w", err)
	}
	if !canManageProduct(actor, sellerID) {
		return productAccessDenied()
	}
	if status != "ARCHIVED" {
		if _, err := tx.Exec(ctx, `UPDATE products SET status = 'ARCHIVED' WHERE id = $1`, id); err != nil {
			return fmt.Errorf("archive product: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit product archive: %w", err)
	}
	return nil
}
