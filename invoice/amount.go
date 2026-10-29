package invoice

import (
	"math"
	"math/big"
	"strconv"
)

// amount keeps invoice money calculations exact until XML formatting.
// Public invoice fields remain float-shaped for compatibility with existing
// callers and database row types.
type amount struct {
	value *big.Rat
}

func amountFromFloat(v float64) amount {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return amount{}
	}

	value, ok := new(big.Rat).SetString(strconv.FormatFloat(v, 'f', -1, 64))
	if !ok {
		return amount{}
	}
	return amount{value: value}
}

func (a amount) rat() *big.Rat {
	if a.value == nil {
		return new(big.Rat)
	}
	return new(big.Rat).Set(a.value)
}

func (a amount) add(b amount) amount {
	return amount{value: new(big.Rat).Add(a.rat(), b.rat())}
}

func (a amount) sub(b amount) amount {
	return amount{value: new(big.Rat).Sub(a.rat(), b.rat())}
}

func (a amount) mul(b amount) amount {
	return amount{value: new(big.Rat).Mul(a.rat(), b.rat())}
}

func (a amount) div(b amount) amount {
	if b.rat().Sign() == 0 {
		return amount{}
	}
	return amount{value: new(big.Rat).Quo(a.rat(), b.rat())}
}

func (a amount) round(places int) amount {
	if places < 0 {
		return a
	}

	factor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(places)), nil)
	scaled := new(big.Rat).Mul(a.rat(), new(big.Rat).SetInt(factor))
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(scaled.Num(), scaled.Denom(), remainder)

	absoluteRemainder := new(big.Int).Abs(remainder)
	if new(big.Int).Lsh(absoluteRemainder, 1).Cmp(scaled.Denom()) >= 0 {
		if scaled.Sign() >= 0 {
			quotient.Add(quotient, big.NewInt(1))
		} else {
			quotient.Sub(quotient, big.NewInt(1))
		}
	}

	return amount{value: new(big.Rat).SetFrac(quotient, factor)}
}

func (a amount) format2() string {
	return a.round(2).rat().FloatString(2)
}

func (a amount) float64() float64 {
	value, err := strconv.ParseFloat(a.rat().FloatString(15), 64)
	if err != nil {
		return 0
	}
	return value
}
