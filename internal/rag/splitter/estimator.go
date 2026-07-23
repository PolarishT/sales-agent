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
		weightedTenths += (ConservativeEstimator{}).runeWeight(current)
	}
	return int(math.Ceil(float64(weightedTenths) / float64((ConservativeEstimator{}).weightScale())))
}

func (ConservativeEstimator) runeWeight(current rune) int {
	switch {
	case unicode.IsSpace(current):
		return 0
	case unicode.Is(unicode.Han, current):
		return 15
	case current <= unicode.MaxASCII &&
		(unicode.IsLetter(current) || unicode.IsDigit(current) || unicode.IsPunct(current)):
		return 3
	default:
		return 15
	}
}

func (ConservativeEstimator) weightScale() int {
	return 10
}
