package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/kolomigor/marketplace-homework2/internal/api"
)

type orderQueries interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type orderProduct struct {
	id     string
	status string
	stock  int64
	price  decimal.Decimal
}

func (s *Service) CreateOrder(ctx context.Context, actor Actor, input api.OrderCreate) (api.OrderResponse, error) {
	var result api.OrderResponse
	if err := validateOrderItems(input.Items); err != nil {
		return result, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	if err = lockOrderUser(ctx, tx, actor.ID); err != nil {
		return result, err
	}
	if err = s.checkOrderRate(ctx, tx, actor.ID, "CREATE_ORDER"); err != nil {
		return result, err
	}
	var active bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM orders WHERE user_id=$1 AND status IN ('CREATED','PAYMENT_PENDING'))`, actor.ID).Scan(&active)
	if err != nil {
		return result, err
	}
	if active {
		return result, Fail(409, "ORDER_HAS_ACTIVE", "У пользователя уже есть активный заказ", nil)
	}
	products, err := lockOrderProducts(ctx, tx, orderProductIDs(input.Items, nil))
	if err != nil {
		return result, err
	}
	if err = checkOrderCatalog(input.Items, products); err != nil {
		return result, err
	}
	if err = checkOrderStock(input.Items, products); err != nil {
		return result, err
	}
	result.Items, result.TotalAmount = makeOrderItems(input.Items, products, nil)
	if err = reserveOrderStock(ctx, tx, input.Items); err != nil {
		return result, err
	}
	result.DiscountAmount = decimal.Zero
	if input.PromoCode != nil {
		promo, promoErr := lockOrderPromoByCode(ctx, tx, *input.PromoCode)
		if promoErr != nil {
			return result, promoErr
		}
		if err = validateOrderPromo(ctx, tx, promo, false); err != nil {
			return result, err
		}
		if result.TotalAmount.LessThan(promo.minAmount) {
			return result, Fail(422, "PROMO_CODE_MIN_AMOUNT", "Сумма заказа ниже минимальной для промокода", map[string]interface{}{"min_order_amount": promo.minAmount, "order_amount": result.TotalAmount})
		}
		result.DiscountAmount, err = calculateDiscount(result.TotalAmount, promo.discountType, promo.value)
		if err != nil {
			return result, err
		}
		result.TotalAmount = result.TotalAmount.Sub(result.DiscountAmount)
		result.PromoCodeId = &promo.id
		if _, err = tx.Exec(ctx, `UPDATE promo_codes SET current_uses=current_uses+1 WHERE id=$1`, promo.id); err != nil {
			return result, err
		}
	}
	if !fitsMoney(result.TotalAmount) || !fitsMoney(result.DiscountAmount) {
		return result, orderAmountError()
	}
	result.Id, result.UserId, result.Status = uuid.NewString(), actor.ID, api.OrderStatus("CREATED")
	err = tx.QueryRow(ctx, `INSERT INTO orders(id,user_id,status,promo_code_id,total_amount,discount_amount) VALUES($1,$2,$3,$4,$5,$6) RETURNING created_at,updated_at`, result.Id, result.UserId, string(result.Status), result.PromoCodeId, result.TotalAmount.StringFixed(2), result.DiscountAmount.StringFixed(2)).Scan(&result.CreatedAt, &result.UpdatedAt)
	if err != nil {
		return result, err
	}
	if err = insertOrderItems(ctx, tx, result.Id, result.Items); err != nil {
		return result, err
	}
	if err = recordOrderOperation(ctx, tx, actor.ID, "CREATE_ORDER"); err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

func (s *Service) GetOrder(ctx context.Context, actor Actor, id string) (api.OrderResponse, error) {
	var result api.OrderResponse
	// A stable read also keeps totals and item snapshots consistent during updates.
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	result, err = readOrder(ctx, tx, id, false)
	if err != nil {
		return result, err
	}
	if err = checkOrderOwner(actor, result.UserId); err != nil {
		return result, err
	}
	result.Items, err = readOrderItems(ctx, tx, id)
	if err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

func (s *Service) UpdateOrder(ctx context.Context, actor Actor, id string, input api.OrderUpdate) (api.OrderResponse, error) {
	var result api.OrderResponse
	if err := validateOrderItems(input.Items); err != nil {
		return result, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	result, err = lockOwnedOrder(ctx, tx, actor, id)
	if err != nil {
		return result, err
	}
	if string(result.Status) != "CREATED" {
		return result, invalidOrderState(string(result.Status), "UPDATE")
	}
	if err = s.checkOrderRate(ctx, tx, result.UserId, "UPDATE_ORDER"); err != nil {
		return result, err
	}
	oldItems, err := readOrderItems(ctx, tx, id)
	if err != nil {
		return result, err
	}
	products, err := lockOrderProducts(ctx, tx, orderProductIDs(input.Items, oldItems))
	if err != nil {
		return result, err
	}
	if err = returnOrderStock(ctx, tx, oldItems, products); err != nil {
		return result, err
	}
	if err = checkOrderCatalog(input.Items, products); err != nil {
		return result, err
	}
	if err = checkOrderStock(input.Items, products); err != nil {
		return result, err
	}
	result.Items, result.TotalAmount = makeOrderItems(input.Items, products, oldItems)
	if err = reserveOrderStock(ctx, tx, input.Items); err != nil {
		return result, err
	}
	result.DiscountAmount = decimal.Zero
	if result.PromoCodeId != nil {
		promo, promoErr := lockOrderPromoByID(ctx, tx, *result.PromoCodeId)
		if promoErr != nil {
			return result, promoErr
		}
		// This order has already consumed its promo slot, including the last one.
		if err = validateOrderPromo(ctx, tx, promo, true); err != nil {
			return result, err
		}
		if result.TotalAmount.LessThan(promo.minAmount) {
			if err = releaseOrderPromo(ctx, tx, promo.id); err != nil {
				return result, err
			}
			result.PromoCodeId = nil
		} else {
			result.DiscountAmount, err = calculateDiscount(result.TotalAmount, promo.discountType, promo.value)
			if err != nil {
				return result, err
			}
			result.TotalAmount = result.TotalAmount.Sub(result.DiscountAmount)
		}
	}
	if !fitsMoney(result.TotalAmount) || !fitsMoney(result.DiscountAmount) {
		return result, orderAmountError()
	}
	if _, err = tx.Exec(ctx, `DELETE FROM order_items WHERE order_id=$1`, id); err != nil {
		return result, err
	}
	if err = insertOrderItems(ctx, tx, id, result.Items); err != nil {
		return result, err
	}
	err = tx.QueryRow(ctx, `UPDATE orders SET promo_code_id=$2,total_amount=$3,discount_amount=$4 WHERE id=$1 RETURNING updated_at`, id, result.PromoCodeId, result.TotalAmount.StringFixed(2), result.DiscountAmount.StringFixed(2)).Scan(&result.UpdatedAt)
	if err != nil {
		return result, err
	}
	if err = recordOrderOperation(ctx, tx, result.UserId, "UPDATE_ORDER"); err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

func (s *Service) CancelOrder(ctx context.Context, actor Actor, id string) (api.OrderResponse, error) {
	var result api.OrderResponse
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	result, err = lockOwnedOrder(ctx, tx, actor, id)
	if err != nil {
		return result, err
	}
	if !allowedOrderTransition(string(result.Status), "CANCELED") {
		return result, invalidOrderState(string(result.Status), "CANCELED")
	}
	result.Items, err = readOrderItems(ctx, tx, id)
	if err != nil {
		return result, err
	}
	products, err := lockOrderProducts(ctx, tx, orderProductIDs(nil, result.Items))
	if err != nil {
		return result, err
	}
	if err = returnOrderStock(ctx, tx, result.Items, products); err != nil {
		return result, err
	}
	if result.PromoCodeId != nil {
		if _, err = lockOrderPromoByID(ctx, tx, *result.PromoCodeId); err != nil {
			return result, err
		}
		if err = releaseOrderPromo(ctx, tx, *result.PromoCodeId); err != nil {
			return result, err
		}
	}
	result.Status = api.OrderStatus("CANCELED")
	err = tx.QueryRow(ctx, `UPDATE orders SET status='CANCELED' WHERE id=$1 RETURNING updated_at`, id).Scan(&result.UpdatedAt)
	if err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

func (s *Service) TransitionOrder(ctx context.Context, actor Actor, id string, status api.OrderStatus) (api.OrderResponse, error) {
	// Cancellation always goes through its inventory/promo transaction.
	if string(status) == "CANCELED" {
		return s.CancelOrder(ctx, actor, id)
	}
	var result api.OrderResponse
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	result, err = lockOwnedOrder(ctx, tx, actor, id)
	if err != nil {
		return result, err
	}
	if !allowedOrderTransition(string(result.Status), string(status)) {
		return result, invalidOrderState(string(result.Status), string(status))
	}
	result.Items, err = readOrderItems(ctx, tx, id)
	if err != nil {
		return result, err
	}
	result.Status = status
	err = tx.QueryRow(ctx, `UPDATE orders SET status=$2 WHERE id=$1 RETURNING updated_at`, id, string(status)).Scan(&result.UpdatedAt)
	if err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

func validateOrderItems(items []api.OrderItemInput) error {
	fields := make([]map[string]interface{}, 0)
	if len(items) < 1 || len(items) > 50 {
		fields = append(fields, map[string]interface{}{"field": "items", "message": "Требуется от 1 до 50 позиций"})
	}
	seen := make(map[string]bool, len(items))
	for i, item := range items {
		productID, parseErr := uuid.Parse(item.ProductId)
		if parseErr != nil {
			fields = append(fields, map[string]interface{}{"field": fmt.Sprintf("items[%d].product_id", i), "message": "Идентификатор товара должен быть UUID"})
		} else {
			// PostgreSQL treats differently formatted UUID strings as the same ID.
			item.ProductId = productID.String()
			items[i].ProductId = item.ProductId
		}
		if item.Quantity < 1 || item.Quantity > 999 {
			fields = append(fields, map[string]interface{}{"field": fmt.Sprintf("items[%d].quantity", i), "message": "Количество должно быть от 1 до 999"})
		}
		if parseErr == nil && seen[item.ProductId] {
			fields = append(fields, map[string]interface{}{"field": fmt.Sprintf("items[%d].product_id", i), "message": "Товар указан более одного раза"})
		}
		if parseErr == nil {
			seen[item.ProductId] = true
		}
	}
	if len(fields) > 0 {
		return Fail(400, "VALIDATION_ERROR", "Некорректные позиции заказа", map[string]interface{}{"fields": fields})
	}
	return nil
}

func checkOrderOwner(actor Actor, owner string) error {
	if actor.Role != "ADMIN" && actor.ID != owner {
		return Fail(403, "ORDER_OWNERSHIP_VIOLATION", "Заказ принадлежит другому пользователю", nil)
	}
	return nil
}

func invalidOrderState(from, to string) error {
	return Fail(409, "INVALID_STATE_TRANSITION", "Операция запрещена в текущем состоянии заказа", map[string]interface{}{"status": from, "requested_status": to})
}

func orderAmountError() error {
	return Fail(400, "VALIDATION_ERROR", "Сумма заказа превышает допустимый предел", map[string]interface{}{"fields": []map[string]interface{}{{"field": "items", "message": "Максимальная сумма заказа 9999999999.99"}}})
}

func lockOrderUser(ctx context.Context, tx pgx.Tx, userID string) error {
	var id string
	return tx.QueryRow(ctx, `SELECT id::text FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&id)
}

func lockOwnedOrder(ctx context.Context, tx pgx.Tx, actor Actor, id string) (api.OrderResponse, error) {
	result, err := readOrder(ctx, tx, id, false)
	if err != nil {
		return result, err
	}
	if err = checkOrderOwner(actor, result.UserId); err != nil {
		return result, err
	}
	// All order mutations use the same user -> order -> products -> promo lock order.
	if err = lockOrderUser(ctx, tx, result.UserId); err != nil {
		return result, err
	}
	return readOrder(ctx, tx, id, true)
}

func (s *Service) checkOrderRate(ctx context.Context, tx pgx.Tx, userID, operation string) error {
	if s.OrderInterval <= 0 {
		return nil
	}
	var last, now time.Time
	err := tx.QueryRow(ctx, `SELECT created_at,clock_timestamp() FROM user_operations WHERE user_id=$1 AND operation_type=$2 ORDER BY created_at DESC LIMIT 1`, userID, operation).Scan(&last, &now)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	remaining := s.OrderInterval - now.Sub(last)
	if remaining > 0 {
		return Fail(429, "ORDER_LIMIT_EXCEEDED", "Превышен лимит частоты операций с заказом", map[string]interface{}{"operation_type": operation, "retry_after_seconds": int(math.Ceil(remaining.Seconds()))})
	}
	return nil
}

func orderProductIDs(newItems []api.OrderItemInput, oldItems []api.OrderItemResponse) []string {
	seen := make(map[string]bool, len(newItems)+len(oldItems))
	for _, item := range newItems {
		seen[item.ProductId] = true
	}
	for _, item := range oldItems {
		seen[item.ProductId] = true
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func lockOrderProducts(ctx context.Context, tx pgx.Tx, ids []string) (map[string]*orderProduct, error) {
	products := make(map[string]*orderProduct, len(ids))
	for _, id := range ids {
		product := &orderProduct{}
		var price string
		err := tx.QueryRow(ctx, `SELECT id::text,status,stock,price::text FROM products WHERE id=$1 FOR UPDATE`, id).Scan(&product.id, &product.status, &product.stock, &price)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		product.price, err = decimal.NewFromString(price)
		if err != nil {
			return nil, err
		}
		products[id] = product
	}
	return products, nil
}

func checkOrderCatalog(items []api.OrderItemInput, products map[string]*orderProduct) error {
	for _, item := range items {
		product, ok := products[item.ProductId]
		if !ok {
			return Fail(404, "PRODUCT_NOT_FOUND", "Товар не найден", map[string]interface{}{"product_id": item.ProductId})
		}
		if product.status != "ACTIVE" {
			return Fail(409, "PRODUCT_INACTIVE", "Товар недоступен для заказа", map[string]interface{}{"product_id": item.ProductId, "status": product.status})
		}
	}
	return nil
}

func checkOrderStock(items []api.OrderItemInput, products map[string]*orderProduct) error {
	shortages := make([]map[string]interface{}, 0)
	for _, item := range items {
		product := products[item.ProductId]
		if product.stock < int64(item.Quantity) {
			shortages = append(shortages, map[string]interface{}{"product_id": item.ProductId, "requested": item.Quantity, "available": product.stock})
		}
	}
	if len(shortages) > 0 {
		return Fail(409, "INSUFFICIENT_STOCK", "Недостаточно товара на складе", map[string]interface{}{"items": shortages})
	}
	return nil
}

func makeOrderItems(input []api.OrderItemInput, products map[string]*orderProduct, oldItems []api.OrderItemResponse) ([]api.OrderItemResponse, decimal.Decimal) {
	oldPrices := make(map[string]decimal.Decimal, len(oldItems))
	for _, item := range oldItems {
		oldPrices[item.ProductId] = item.PriceAtOrder
	}
	items := make([]api.OrderItemResponse, 0, len(input))
	total := decimal.Zero
	for _, item := range input {
		price := products[item.ProductId].price
		if snapshot, ok := oldPrices[item.ProductId]; ok {
			price = snapshot
		}
		items = append(items, api.OrderItemResponse{Id: uuid.NewString(), ProductId: item.ProductId, Quantity: item.Quantity, PriceAtOrder: price})
		total = total.Add(price.Mul(decimal.NewFromInt(int64(item.Quantity))))
	}
	return items, total.Round(2)
}

func reserveOrderStock(ctx context.Context, tx pgx.Tx, items []api.OrderItemInput) error {
	for _, item := range items {
		if _, err := tx.Exec(ctx, `UPDATE products SET stock=stock-$2 WHERE id=$1`, item.ProductId, item.Quantity); err != nil {
			return err
		}
	}
	return nil
}

func returnOrderStock(ctx context.Context, tx pgx.Tx, items []api.OrderItemResponse, products map[string]*orderProduct) error {
	for _, item := range items {
		product := products[item.ProductId]
		if product == nil {
			return fmt.Errorf("reserved product %s is missing", item.ProductId)
		}
		if product.stock+int64(item.Quantity) > math.MaxInt32 {
			return Fail(400, "VALIDATION_ERROR", "Возврат товара превышает допустимый остаток", map[string]interface{}{"fields": []map[string]interface{}{{"field": "stock", "message": "Возврат превышает максимально допустимый остаток", "product_id": item.ProductId, "maximum_stock": math.MaxInt32}}})
		}
		if _, err := tx.Exec(ctx, `UPDATE products SET stock=stock+$2 WHERE id=$1`, item.ProductId, item.Quantity); err != nil {
			return err
		}
		product.stock += int64(item.Quantity)
	}
	return nil
}

func insertOrderItems(ctx context.Context, tx pgx.Tx, orderID string, items []api.OrderItemResponse) error {
	for _, item := range items {
		if _, err := tx.Exec(ctx, `INSERT INTO order_items(id,order_id,product_id,quantity,price_at_order) VALUES($1,$2,$3,$4,$5)`, item.Id, orderID, item.ProductId, item.Quantity, item.PriceAtOrder.StringFixed(2)); err != nil {
			return err
		}
	}
	return nil
}

func recordOrderOperation(ctx context.Context, tx pgx.Tx, userID, operation string) error {
	_, err := tx.Exec(ctx, `INSERT INTO user_operations(id,user_id,operation_type,created_at) VALUES($1,$2,$3,clock_timestamp())`, uuid.NewString(), userID, operation)
	return err
}

func readOrder(ctx context.Context, q orderQueries, id string, lock bool) (api.OrderResponse, error) {
	var result api.OrderResponse
	var amount, discount string
	query := `SELECT id::text,user_id::text,status,promo_code_id::text,total_amount::text,discount_amount::text,created_at,updated_at FROM orders WHERE id=$1`
	if lock {
		query += " FOR UPDATE"
	}
	var status string
	err := q.QueryRow(ctx, query, id).Scan(&result.Id, &result.UserId, &status, &result.PromoCodeId, &amount, &discount, &result.CreatedAt, &result.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, Fail(404, "ORDER_NOT_FOUND", "Заказ не найден", map[string]interface{}{"order_id": id})
	}
	if err != nil {
		return result, err
	}
	result.Status = api.OrderStatus(status)
	result.TotalAmount, err = decimal.NewFromString(amount)
	if err != nil {
		return result, err
	}
	result.DiscountAmount, err = decimal.NewFromString(discount)
	return result, err
}

func readOrderItems(ctx context.Context, q orderQueries, id string) ([]api.OrderItemResponse, error) {
	rows, err := q.Query(ctx, `SELECT id::text,product_id::text,quantity,price_at_order::text FROM order_items WHERE order_id=$1 ORDER BY product_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]api.OrderItemResponse, 0)
	for rows.Next() {
		var item api.OrderItemResponse
		var price string
		if err = rows.Scan(&item.Id, &item.ProductId, &item.Quantity, &price); err != nil {
			return nil, err
		}
		item.PriceAtOrder, err = decimal.NewFromString(price)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
