// Package fixed provides checked fixed-point arithmetic for the
// thermal-vacuum domain.
//
// Conventions (see specification, domain rules):
//   - temperature in milliKelvin,
//   - pressure in milliPascal,
//   - time in milliseconds,
//   - ratios in parts per million (ppm).
//
// Every multiply/add/subtract/divide checks for overflow, negative intervals,
// and division by zero, and rounds in the direction that is unfavourable to a
// passing judgement (so a marginal reading never passes by rounding error).
package fixed

import (
	"math"

	"thermal-vacuum-test-gate/internal/domain"
)

// Temperature is milliKelvin.
type Temperature int64

// Pressure is milliPascal.
type Pressure int64

// Duration is milliseconds.
type Duration int64

// Ratio is parts per million.
type Ratio int64

// Add returns a+b or a domain overflow error.
func Add(a, b int64) (int64, error) {
	if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
		return 0, domain.NewError(domain.CodeOverflow, "fixed-point add overflow")
	}
	return a + b, nil
}

// Sub returns a-b or a domain overflow error.
func Sub(a, b int64) (int64, error) {
	if (b < 0 && a > math.MaxInt64+b) || (b > 0 && a < math.MinInt64+b) {
		return 0, domain.NewError(domain.CodeOverflow, "fixed-point subtract overflow")
	}
	return a - b, nil
}

// Mul returns a*b or a domain overflow error.
func Mul(a, b int64) (int64, error) {
	if a == 0 || b == 0 {
		return 0, nil
	}
	if a == math.MinInt64 && b == -1 {
		return 0, domain.NewError(domain.CodeOverflow, "fixed-point multiply overflow")
	}
	r := a * b
	if r/b != a {
		return 0, domain.NewError(domain.CodeOverflow, "fixed-point multiply overflow")
	}
	return r, nil
}

// Rate returns numerator/denominator in ppm, rounded away from zero so that a
// rate is never understated when compared against a limit. denominator must be
// a positive duration in milliseconds.
func Rate(numerator int64, denominator Duration) (Ratio, error) {
	if denominator <= 0 {
		return 0, domain.NewError(domain.CodeNonPositiveInterval, "rate interval must be positive")
	}
	q := numerator / int64(denominator)
	r := numerator % int64(denominator)
	if r != 0 {
		// Round away from zero: the unfavourable direction for a rate limit.
		if numerator > 0 {
			q++
		} else {
			q--
		}
	}
	return Ratio(q), nil
}

// Range returns max-min over the supplied readings, or an overflow error.
func Range(values ...int64) (int64, error) {
	if len(values) == 0 {
		return 0, nil
	}
	min, max := values[0], values[0]
	for _, v := range values[1:] {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	return Sub(max, min)
}

// Drift returns the per-interval drift in ppm as (max-min)/duration, rounded
// away from zero.
func Drift(min, max int64, duration Duration) (Ratio, error) {
	spread, err := Sub(max, min)
	if err != nil {
		return 0, err
	}
	return Rate(spread, duration)
}

// Coverage returns samples*1e6/required in ppm, rounded down so a marginal
// coverage never appears sufficient.
func Coverage(samples, required int64) (Ratio, error) {
	if required <= 0 {
		return 0, domain.NewError(domain.CodeInvalidRange, "required samples must be positive")
	}
	if samples < 0 {
		return 0, domain.NewError(domain.CodeInvalidRange, "samples must be non-negative")
	}
	num, err := Mul(samples, 1_000_000)
	if err != nil {
		return 0, err
	}
	return Ratio(num / required), nil
}
