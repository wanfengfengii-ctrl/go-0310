package domain

import "fmt"

// Code is a stable, machine-readable error code. HTTP responses map codes to
// fixed status values, and clients rely on the codes never changing.
type Code string

const (
	CodeDependencyCycle      Code = "plan_dependency_cycle"
	CodeDuplicateSensor      Code = "plan_duplicate_sensor"
	CodeInvalidRange         Code = "plan_invalid_range"
	CodeStaleCalibration     Code = "plan_stale_calibration"
	CodeMissingCalibration   Code = "plan_missing_calibration"
	CodeOverflow             Code = "fixed_overflow"
	CodeDivisionByZero       Code = "fixed_division_by_zero"
	CodeNonPositiveInterval  Code = "fixed_non_positive_interval"
	CodeTimeRegression       Code = "measurement_time_regression"
	CodeIdempotencyConflict  Code = "idempotency_conflict"
	CodeLeaseConflict        Code = "lease_conflict"
	CodeLeaseExpired         Code = "lease_expired"
	CodeGenerationConflict   Code = "retest_generation_conflict"
	CodeVerdictConflict      Code = "verdict_conflict"
	CodeDuplicateReview      Code = "review_duplicate"
	CodeInvalidStage         Code = "stage_invalid"
	CodeBaselineMissing      Code = "baseline_missing"
	CodeInsufficientEvidence Code = "evidence_insufficient"
	CodeNotQualified         Code = "reviewer_not_qualified"
	CodePlanNotFound         Code = "plan_not_found"
	CodeRunNotFound          Code = "run_not_found"
	CodeConflict             Code = "conflict"
	CodeInternal             Code = "internal"

	// Additional failure boundaries surfaced across the workflow engines.
	CodeStageNotReached    Code = "stage_not_reached"
	CodeVerdictNotReady    Code = "verdict_not_ready"
	CodeRunFrozen          Code = "run_frozen"
	CodeIdempotencyMissing Code = "idempotency_key_missing"
	CodeEquipmentNotFound  Code = "equipment_not_found"
	CodeInvalidGeneration  Code = "invalid_generation"
	CodeRunCompleted       Code = "run_completed"
	CodeBaselineInvalid    Code = "baseline_invalid"
)

// Error is a domain error carrying a stable code and an ordered list of
// reasons. The reason order is part of the contract and must be deterministic.
type Error struct {
	Code    Code
	Message string
	Reasons []string
}

func (e *Error) Error() string {
	if len(e.Reasons) == 0 {
		return string(e.Code) + ": " + e.Message
	}
	return string(e.Code) + ": " + e.Message + " (" + fmt.Sprintf("%v", e.Reasons) + ")"
}

// NewError builds a domain error with a single reason.
func NewError(code Code, message string) *Error {
	return &Error{Code: code, Message: message}
}

// WithReasons returns a copy of the error carrying deterministic reasons.
func (e *Error) WithReasons(reasons ...string) *Error {
	cp := *e
	cp.Reasons = append([]string(nil), reasons...)
	return &cp
}

// Is reports whether err carries the given code.
func Is(err error, code Code) bool {
	if e, ok := err.(*Error); ok {
		return e.Code == code
	}
	return false
}
