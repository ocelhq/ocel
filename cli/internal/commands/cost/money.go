package cost

import (
	"github.com/shopspring/decimal"
)

const moneyPlaces = 2

func amount(text string) decimal.Decimal {
	parsed, err := decimal.NewFromString(text)
	if err != nil {
		return decimal.Zero
	}
	return parsed
}

func sum(amounts ...string) string {
	total := decimal.Zero
	for _, each := range amounts {
		total = total.Add(amount(each))
	}
	return total.StringFixed(moneyPlaces)
}

func changed(text string) string {
	if text == "" {
		return "unknown"
	}
	return signed(text)
}

func signed(text string) string {
	value := amount(text)
	if value.IsNegative() {
		return value.StringFixed(moneyPlaces)
	}
	return "+" + value.StringFixed(moneyPlaces)
}
