package budgets

import (
	"errors"
	"math/big"
)

// PriceMicros rounds a token charge up once, without floating point or overflow.
// Input tokens include cached tokens; cached input is charged at its own rate.
func PriceMicros(input, output, cache, inputRate, outputRate, cacheRate int64) (int64, error) {
	if input < 0 || output < 0 || cache < 0 || cache > input || inputRate < 0 || outputRate < 0 || cacheRate < 0 {
		return 0, errors.New("invalid token price")
	}
	total := new(big.Int)
	for _, pair := range [][2]int64{{input - cache, inputRate}, {output, outputRate}, {cache, cacheRate}} {
		total.Add(total, new(big.Int).Mul(big.NewInt(pair[0]), big.NewInt(pair[1])))
	}
	total.Add(total, big.NewInt(999999))
	total.Div(total, big.NewInt(1000000))
	if !total.IsInt64() {
		return 0, errors.New("token cost overflow")
	}
	return total.Int64(), nil
}

// ReportedMicros converts provider-reported USD to integer microdollars, rounding up.
func ReportedMicros(value string) (int64, error) {
	amount, ok := new(big.Rat).SetString(value)
	if !ok || amount.Sign() < 0 {
		return 0, errors.New("invalid provider cost")
	}
	amount.Mul(amount, big.NewRat(1000000, 1))
	rounded, remainder := new(big.Int), new(big.Int)
	rounded.QuoRem(amount.Num(), amount.Denom(), remainder)
	if remainder.Sign() > 0 {
		rounded.Add(rounded, big.NewInt(1))
	}
	if !rounded.IsInt64() {
		return 0, errors.New("provider cost overflow")
	}
	return rounded.Int64(), nil
}
