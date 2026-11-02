package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/kolomigor/marketplace-homework2/internal/api"
)

type orderPromo struct {
	id           string
	discountType string
	value        decimal.Decimal
	minAmount    decimal.Decimal
	maxUses      int
	currentUses  int
	validFrom    time.Time
	validUntil   time.Time
	active       bool
}

func (s *Service) CreatePromoCode(ctx context.Context, actor Actor, input api.PromoCodeCreate) (api.PromoCodeResponse, error) {
	var result api.PromoCodeResponse
	if actor.Role != "SELLER" && actor.Role != "ADMIN" {
		return result, Fail(403, "ACCESS_DENIED", "Недостаточно прав для создания промокода", nil)
	}
	fields := make([]map[string]interface{}, 0)
	if !input.ValidUntil.After(input.ValidFrom) {
		fields = append(fields, map[string]interface{}{"field": "valid_until", "message": "Конец действия должен быть позже начала"})
	}
	if !fitsMoney(input.DiscountValue) || !input.DiscountValue.IsPositive() {
		fields = append(fields, map[string]interface{}{"field": "discount_value", "message": "Скидка должна быть от 0.01 до 9999999999.99"})
	}
	if !fitsMoney(input.MinOrderAmount) {
		fields = append(fields, map[string]interface{}{"field": "min_order_amount", "message": "Минимальная сумма должна быть от 0 до 9999999999.99"})
	}
	if len(fields) > 0 {
		return result, Fail(400, "VALIDATION_ERROR", "Некорректный промокод", map[string]interface{}{"fields": fields})
	}
	active := true
	if input.Active != nil {
		active = *input.Active
	}
	result = api.PromoCodeResponse{
		Id:             uuid.NewString(),
		Code:           input.Code,
		DiscountType:   input.DiscountType,
		DiscountValue:  input.DiscountValue.Round(2),
		MinOrderAmount: input.MinOrderAmount.Round(2),
		MaxUses:        input.MaxUses,
		CurrentUses:    0,
		ValidFrom:      input.ValidFrom,
		ValidUntil:     input.ValidUntil,
		Active:         active,
	}
	_, err := s.DB.Exec(ctx, `INSERT INTO promo_codes(id,code,discount_type,discount_value,min_order_amount,max_uses,current_uses,valid_from,valid_until,active) VALUES($1,$2,$3,$4,$5,$6,0,$7,$8,$9)`, result.Id, result.Code, string(result.DiscountType), result.DiscountValue.StringFixed(2), result.MinOrderAmount.StringFixed(2), result.MaxUses, result.ValidFrom, result.ValidUntil, result.Active)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return result, Fail(409, "PROMO_CODE_ALREADY_EXISTS", "Промокод с таким кодом уже существует", map[string]interface{}{"code": result.Code})
	}
	return result, err
}

func lockOrderPromoByCode(ctx context.Context, tx pgx.Tx, code string) (orderPromo, error) {
	return readLockedOrderPromo(ctx, tx, "code", code)
}

func lockOrderPromoByID(ctx context.Context, tx pgx.Tx, id string) (orderPromo, error) {
	return readLockedOrderPromo(ctx, tx, "id", id)
}

func readLockedOrderPromo(ctx context.Context, tx pgx.Tx, column, value string) (orderPromo, error) {
	var result orderPromo
	var discount, minimum string
	// column is only passed by the two private wrappers above.
	err := tx.QueryRow(ctx, `SELECT id::text,discount_type,discount_value::text,min_order_amount::text,max_uses,current_uses,valid_from,valid_until,active FROM promo_codes WHERE `+column+`=$1 FOR UPDATE`, value).Scan(&result.id, &result.discountType, &discount, &minimum, &result.maxUses, &result.currentUses, &result.validFrom, &result.validUntil, &result.active)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, Fail(422, "PROMO_CODE_INVALID", "Промокод не найден", nil)
	}
	if err != nil {
		return result, err
	}
	result.value, err = decimal.NewFromString(discount)
	if err != nil {
		return result, err
	}
	result.minAmount, err = decimal.NewFromString(minimum)
	return result, err
}

func validateOrderPromo(ctx context.Context, tx pgx.Tx, promo orderPromo, alreadyUsed bool) error {
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return err
	}
	exhausted := promo.currentUses >= promo.maxUses
	if alreadyUsed {
		exhausted = promo.currentUses > promo.maxUses
	}
	if !promo.active || exhausted || now.Before(promo.validFrom) || now.After(promo.validUntil) {
		return Fail(422, "PROMO_CODE_INVALID", "Промокод неактивен, истёк или исчерпан", nil)
	}
	return nil
}

func releaseOrderPromo(ctx context.Context, tx pgx.Tx, id string) error {
	result, err := tx.Exec(ctx, `UPDATE promo_codes SET current_uses=current_uses-1 WHERE id=$1 AND current_uses>0`, id)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("promo usage invariant violated for %s", id)
	}
	return nil
}
