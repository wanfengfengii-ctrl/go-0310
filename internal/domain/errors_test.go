package domain

import "testing"

func TestErrorIsMatchesCode(t *testing.T) {
	err := NewError(CodeLeaseConflict, "already held")
	if !Is(err, CodeLeaseConflict) {
		t.Fatalf("Is should match CodeLeaseConflict")
	}
	if Is(err, CodeVerdictConflict) {
		t.Fatalf("Is should not match CodeVerdictConflict")
	}
}

func TestErrorWithReasonsPreservesOrder(t *testing.T) {
	err := NewError(CodeInvalidRange, "validation failed").WithReasons("a", "b")
	got := err.Reasons
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("reasons = %v; want [a b]", got)
	}
}
