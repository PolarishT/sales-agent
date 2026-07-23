package splitter

import (
	"math"
	"unicode"
)

type TokenEstimator interface {
	Estimate(string) int
}

type ConservativeEstimator struct{}

func (ConservativeEstimator) Estimate(value string) int {
	weightedTenths := 0
	for _, current := range value {
		switch {
		case unicode.IsSpace(current):
			continue
		case unicode.Is(unicode.Han, current):
			weightedTenths += 15
		case current <= unicode.MaxASCII &&
			(unicode.IsLetter(current) || unicode.IsDigit(current) || unicode.IsPunct(current)):
			weightedTenths += 3
		default:
			weightedTenths += 15
		}
	}
	return int(math.Ceil(float64(weightedTenths) / 10))
}
