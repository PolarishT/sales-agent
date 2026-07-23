package httpapi

import (
	"math"
	"testing"
)

func TestMultipartRequestBodySizeAddsBoundedEnvelopeAllowance(t *testing.T) {
	const maxUploadBytes = int64(5 << 20)
	got, err := MultipartRequestBodySize(maxUploadBytes)
	if err != nil {
		t.Fatal(err)
	}
	const want = 6 << 20
	if got != want {
		t.Fatalf("MultipartRequestBodySize() = %d, want %d", got, want)
	}
}

func TestMultipartRequestBodySizeRejectsInvalidAndOverflowingLimits(t *testing.T) {
	for _, value := range []int64{0, -1, math.MaxInt64} {
		t.Run(stringValue(value), func(t *testing.T) {
			if _, err := MultipartRequestBodySize(value); err == nil {
				t.Fatalf("MultipartRequestBodySize(%d) error = nil", value)
			}
		})
	}
}

func TestMultipartRequestBodySizeAcceptsLargestConvertibleLimit(t *testing.T) {
	maxInt := int64(^uint(0) >> 1)
	maxUploadBytes := maxInt - multipartRequestOverheadBytes
	got, err := MultipartRequestBodySize(maxUploadBytes)
	if err != nil {
		t.Fatal(err)
	}
	if int64(got) != maxInt {
		t.Fatalf("MultipartRequestBodySize() = %d, want %d", got, maxInt)
	}
	if _, err := MultipartRequestBodySize(maxUploadBytes + 1); err == nil {
		t.Fatal("overflowing upload limit was accepted")
	}
}

func stringValue(value int64) string {
	if value < 0 {
		return "negative"
	}
	if value == 0 {
		return "zero"
	}
	return "overflow"
}
