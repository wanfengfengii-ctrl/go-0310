package fixed

import (
	"math"
	"testing"

	"thermal-vacuum-test-gate/internal/domain"
)

func TestAddSubMulOverflow(t *testing.T) {
	if _, err := Add(math.MaxInt64, 1); !domain.Is(err, domain.CodeOverflow) {
		t.Fatalf("Add overflow not detected: %v", err)
	}
	if _, err := Sub(math.MinInt64, 1); !domain.Is(err, domain.CodeOverflow) {
		t.Fatalf("Sub overflow not detected: %v", err)
	}
	if _, err := Mul(math.MaxInt64, 2); !domain.Is(err, domain.CodeOverflow) {
		t.Fatalf("Mul overflow not detected: %v", err)
	}
	if got, err := Mul(math.MinInt64, -1); err == nil || got != 0 {
		t.Fatalf("Mul MinInt64*-1 expected overflow, got %d, %v", got, err)
	}
}

func TestRateAdverseRounding(t *testing.T) {
	// 5/2 rounds away from zero to 3, not 2.
	got, err := Rate(5, 2)
	if err != nil || got != 3 {
		t.Fatalf("Rate(5,2) = %d, %v; want 3", got, err)
	}
	// -5/2 rounds away from zero to -3.
	got, err = Rate(-5, 2)
	if err != nil || got != -3 {
		t.Fatalf("Rate(-5,2) = %d, %v; want -3", got, err)
	}
	// Non-positive interval is rejected.
	if _, err := Rate(5, 0); !domain.Is(err, domain.CodeNonPositiveInterval) {
		t.Fatalf("Rate zero interval not rejected: %v", err)
	}
}

func TestCoverageRoundDown(t *testing.T) {
	// 1 sample of 3 required = 333333 ppm (rounded down, not up).
	got, err := Coverage(1, 3)
	if err != nil || got != 333333 {
		t.Fatalf("Coverage(1,3) = %d, %v; want 333333", got, err)
	}
	if _, err := Coverage(1, 0); !domain.Is(err, domain.CodeInvalidRange) {
		t.Fatalf("Coverage zero required not rejected: %v", err)
	}
}

func TestRangeAndDrift(t *testing.T) {
	got, err := Range(3, 9, 4, 1)
	if err != nil || got != 8 {
		t.Fatalf("Range = %d, %v; want 8", got, err)
	}
	drift, err := Drift(1, 9, 4)
	if err != nil || drift != 2 {
		t.Fatalf("Drift = %d, %v; want 2", drift, err)
	}
}
