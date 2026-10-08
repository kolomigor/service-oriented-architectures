package service

import (
	"testing"

	"github.com/shopspring/decimal"

	"github.com/kolomigor/marketplace-homework2/internal/api"
)

func TestCalculateDiscount(t *testing.T) {
	tests := []struct {
		name, amount, kind, value, want string
	}{
		{"percentage uses decimal arithmetic", "0.30", "PERCENTAGE", "10", "0.03"},
		{"percentage rounding", "12.35", "PERCENTAGE", "10", "1.24"},
		{"70 percent boundary", "100", "PERCENTAGE", "70", "70"},
		{"one cent cannot receive 100 percent discount", "0.01", "PERCENTAGE", "70", "0"},
		{"rounding cannot exceed 70 percent", "1.01", "PERCENTAGE", "70", "0.70"},
		{"more than 70 means full price", "100", "PERCENTAGE", "70.01", "0"},
		{"fixed discount capped by amount", "25", "FIXED_AMOUNT", "40", "25"},
		{"fixed discount below amount", "100", "FIXED_AMOUNT", "12.50", "12.50"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := calculateDiscount(decimal.RequireFromString(test.amount), test.kind, decimal.RequireFromString(test.value))
			if err != nil {
				t.Fatal(err)
			}
			if !got.Equal(decimal.RequireFromString(test.want)) {
				t.Fatalf("discount = %s, want %s", got, test.want)
			}
			if test.kind == "PERCENTAGE" && got.GreaterThan(decimal.RequireFromString(test.amount).Mul(decimal.RequireFromString("0.70"))) {
				t.Fatalf("discount %s exceeds 70%% of %s", got, test.amount)
			}
		})
	}
}

func TestMakeOrderItemsPreservesExistingPriceSnapshots(t *testing.T) {
	products := map[string]*orderProduct{
		"existing": {price: decimal.RequireFromString("999.99")},
		"new":      {price: decimal.RequireFromString("20.30")},
	}
	old := []api.OrderItemResponse{{ProductId: "existing", PriceAtOrder: decimal.RequireFromString("10.10")}}
	items, total := makeOrderItems([]api.OrderItemInput{{ProductId: "existing", Quantity: 3}, {ProductId: "new", Quantity: 2}}, products, old)
	if !items[0].PriceAtOrder.Equal(decimal.RequireFromString("10.10")) {
		t.Fatalf("existing snapshot changed to %s", items[0].PriceAtOrder)
	}
	if !items[1].PriceAtOrder.Equal(products["new"].price) {
		t.Fatalf("new item snapshot = %s", items[1].PriceAtOrder)
	}
	if !total.Equal(decimal.RequireFromString("70.90")) {
		t.Fatalf("total = %s, want 70.90", total)
	}
}

func TestAllowedOrderTransitions(t *testing.T) {
	states := []string{"CREATED", "PAYMENT_PENDING", "PAID", "SHIPPED", "COMPLETED", "CANCELED"}
	allowed := map[string]bool{
		"CREATED/PAYMENT_PENDING":  true,
		"CREATED/CANCELED":         true,
		"PAYMENT_PENDING/PAID":     true,
		"PAYMENT_PENDING/CANCELED": true,
		"PAID/SHIPPED":             true,
		"SHIPPED/COMPLETED":        true,
	}
	for _, from := range states {
		for _, to := range states {
			if got := allowedOrderTransition(from, to); got != allowed[from+"/"+to] {
				t.Errorf("transition %s -> %s = %v", from, to, got)
			}
		}
	}
}

func TestMoneyDatabaseBoundary(t *testing.T) {
	for _, test := range []struct {
		amount string
		want   bool
	}{{"0", true}, {"9999999999.99", true}, {"10000000000", false}, {"-0.01", false}} {
		if got := fitsMoney(decimal.RequireFromString(test.amount)); got != test.want {
			t.Errorf("fitsMoney(%s) = %v", test.amount, got)
		}
	}
}
