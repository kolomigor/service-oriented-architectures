package service

import (
	"fmt"

	"github.com/shopspring/decimal"
)

var maxMoney = decimal.RequireFromString("9999999999.99")

// calculateDiscount keeps all intermediate calculations in decimal arithmetic.
// The task's percentage rule grants no discount if the rate exceeds 70%.
// After rounding to cents, the discount still cannot exceed 70% of the amount.
func calculateDiscount(amount decimal.Decimal, discountType string, value decimal.Decimal) (decimal.Decimal, error) {
	if amount.IsNegative() || value.IsNegative() {
		return decimal.Zero, fmt.Errorf("negative monetary amount")
	}
	var discount decimal.Decimal
	switch discountType {
	case "PERCENTAGE":
		if value.GreaterThan(decimal.NewFromInt(70)) {
			return decimal.Zero, nil
		}
		discount = amount.Mul(value).Div(decimal.NewFromInt(100))
		cap := amount.Mul(decimal.RequireFromString("0.70")).Truncate(2)
		return decimal.Min(discount.Round(2), cap), nil
	case "FIXED_AMOUNT":
		discount = decimal.Min(value, amount)
	default:
		return decimal.Zero, fmt.Errorf("unknown discount type: %s", discountType)
	}
	return decimal.Min(discount.Round(2), amount), nil
}

func fitsMoney(amount decimal.Decimal) bool {
	return !amount.IsNegative() && !amount.GreaterThan(maxMoney)
}

func allowedOrderTransition(from, to string) bool {
	switch from {
	case "CREATED":
		return to == "PAYMENT_PENDING" || to == "CANCELED"
	case "PAYMENT_PENDING":
		return to == "PAID" || to == "CANCELED"
	case "PAID":
		return to == "SHIPPED"
	case "SHIPPED":
		return to == "COMPLETED"
	default:
		return false
	}
}
