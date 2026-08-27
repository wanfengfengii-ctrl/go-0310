// Package domain defines the stable domain types, identifiers, and error
// codes shared across the thermal-vacuum test gate service.
//
// The types here correspond one-to-one with the data model described in the
// project specification and form the contract between the HTTP API, the
// persistence layer, and the business-flow engines. Values are intentionally
// plain structs: validation and state transitions live in the owning
// components so the domain layer stays free of side effects.
package domain

// StageName enumerates the ordered workflow stages of a thermal-vacuum run.
type StageName string

const (
	StageEvacuate     StageName = "evacuate"     // 抽真空
	StageColdRamp     StageName = "cold_ramp"    // 低温降温
	StageColdSoak     StageName = "cold_soak"    // 低温稳态
	StageHotRamp      StageName = "hot_ramp"     // 高温升温
	StageHotSoak      StageName = "hot_soak"     // 高温稳态
	StageReturnAmb    StageName = "return_amb"   // 回常温
	StageRepressurize StageName = "repressurize" // 复压

	// StageBaseline is the synthetic pre-flight stage before evacuation. It is
	// not part of the locked plan dependency graph but is tracked so that the
	// run aggregate has a well-defined starting position.
	StageBaseline StageName = "baseline"
)

// CanonicalStages is the fixed workflow order every thermal-vacuum run follows
// after baseline completion. The cold/hot ramp+soak pair is repeated once per
// cycle; evacuation, return-to-ambient and repressurisation happen once.
var CanonicalStages = []StageName{
	StageEvacuate,
	StageColdRamp,
	StageColdSoak,
	StageHotRamp,
	StageHotSoak,
	StageReturnAmb,
	StageRepressurize,
}

// IsSoak reports whether the stage requires a steady-state evidence window.
func (s StageName) IsSoak() bool {
	return s == StageColdSoak || s == StageHotSoak
}

// IsCycleStage reports whether the stage is part of the repeated cycle body.
func (s StageName) IsCycleStage() bool {
	switch s {
	case StageColdRamp, StageColdSoak, StageHotRamp, StageHotSoak:
		return true
	default:
		return false
	}
}

// EquipmentType classifies the physical devices the service must lease.
type EquipmentType string

const (
	EquipmentChamber     EquipmentType = "chamber"      // 舱体
	EquipmentThermostat  EquipmentType = "thermostat"   // 温控器
	EquipmentVacuumGauge EquipmentType = "vacuum_gauge" // 真空计
	EquipmentCollector   EquipmentType = "collector"    // 采集器
)

// SensorSpec describes a single measurement point in a locked plan.
type SensorSpec struct {
	ID          string `json:"id"`
	Group       string `json:"group"`        // 测点分组
	CollectorID string `json:"collector_id"` // 共享采集器
}

// StageSpec describes one ordered workflow stage and its pass criteria.
type StageSpec struct {
	Name         StageName   `json:"name"`
	Sequence     int         `json:"sequence"`     // 1-based stage order
	Dependencies []StageName `json:"dependencies"` // locked stage dependency edges
	// Targets are expressed in fixed-point units (see package fixed).
	VacuumTargetMilliPa   int64 `json:"vacuum_target_milli_pa,omitempty"`
	ColdTargetMilliKelvin int64 `json:"cold_target_milli_kelvin,omitempty"`
	HotTargetMilliKelvin  int64 `json:"hot_target_milli_kelvin,omitempty"`
	RampRatePPM           int64 `json:"ramp_rate_ppm,omitempty"` // 升降温速率，百万分比
	SoakWindowMillis      int64 `json:"soak_window_millis,omitempty"`
	RequiredSamples       int64 `json:"required_samples,omitempty"`
	// Steady-state pass limits.
	MaxRangeMilliKelvin int64 `json:"max_range_milli_kelvin,omitempty"`
	MaxDriftPPM         int64 `json:"max_drift_ppm,omitempty"`
	MaxPressureMilliPa  int64 `json:"max_pressure_milli_pa,omitempty"`
}

// TestPlan is the immutable, locked plan version for one specimen.
type TestPlan struct {
	ID                          string       `json:"id"`
	Version                     int          `json:"version"`
	SpecimenID                  string       `json:"specimen_id"`
	Sensors                     []SensorSpec `json:"sensors"`
	Stages                      []StageSpec  `json:"stages"`
	Cycles                      int          `json:"cycles"`
	CalibrationSummary          string       `json:"calibration_summary"` // 设备校准摘要
	CalibrationValidUntilMillis int64        `json:"calibration_valid_until_millis,omitempty"`
	LockedAtMillis              int64        `json:"locked_at_millis"`
	// Ambient (return-to-room) criteria for the final environmental recovery.
	AmbientTempMilliKelvin      int64 `json:"ambient_temp_milli_kelvin,omitempty"`
	AmbientToleranceMilliKelvin int64 `json:"ambient_tolerance_milli_kelvin,omitempty"`
	AmbientPressureMilliPa      int64 `json:"ambient_pressure_milli_pa,omitempty"`
}

// TestRun is the aggregate root for a single thermal-vacuum execution.
type TestRun struct {
	ID              string    `json:"id"`
	PlanID          string    `json:"plan_id"`
	PlanVersion     int       `json:"plan_version"`
	Generation      int       `json:"generation"`       // 试验代次
	Stage           StageName `json:"stage"`            // 当前阶段
	CurrentCycle    int       `json:"current_cycle"`    // 当前循环编号，1-based
	CompletedCycles int       `json:"completed_cycles"` // 连续完成循环前缀的长度
	BaselineDone    bool      `json:"baseline_done"`
	Frozen          bool      `json:"frozen"`
	FreezeReason    string    `json:"freeze_reason,omitempty"`
	Completed       bool      `json:"completed"`
	EventSeq        int64     `json:"event_seq"` // 单调事件序号
	CreatedAtMillis int64     `json:"created_at_millis"`
}

// RunEvent is an append-only event persisted for a run.
type RunEvent struct {
	Seq      int64  `json:"seq"`
	RunID    string `json:"run_id"`
	Type     string `json:"type"`
	Payload  []byte `json:"payload,omitempty"`
	AtMillis int64  `json:"at_millis"`
}

// CycleState captures the continuous completed-cycle prefix and evidence.
type CycleState struct {
	RunID           string `json:"run_id"`
	CompletedCycles int    `json:"completed_cycles"`
	CurrentCycle    int    `json:"current_cycle"`
}

// Lease is a time-bounded mutual-exclusion hold on a piece of equipment.
type Lease struct {
	ID               string `json:"id"`
	EquipmentID      string `json:"equipment_id"`
	Holder           string `json:"holder"`
	Token            string `json:"token"`
	ValidUntilMillis int64  `json:"valid_until_millis"`
}

// MeasurementCall records one scripted acquisition attempt and its outcome.
type MeasurementCall struct {
	ID             string `json:"id"`
	Attempt        int    `json:"attempt"` // deterministic 1-based attempt number
	EquipmentID    string `json:"equipment_id"`
	Success        bool   `json:"success"`
	FailureType    string `json:"failure_type,omitempty"` // timeout|disconnect|expired_cal|format_error|expired_token
	PayloadSummary string `json:"payload_summary,omitempty"`
}

// Measurement is a single point reading bound to plan/run/stage/cycle.
type Measurement struct {
	ID                     string    `json:"id"`
	RunID                  string    `json:"run_id"`
	Generation             int       `json:"generation"`
	Stage                  StageName `json:"stage"`
	Cycle                  int       `json:"cycle"`
	SensorID               string    `json:"sensor_id"`
	TemperatureMilliKelvin int64     `json:"temperature_milli_kelvin"`
	PressureMilliPa        int64     `json:"pressure_milli_pa"`
	AtMillis               int64     `json:"at_millis"`
	Valid                  bool      `json:"valid"`
}

// EvidenceWindow summarises the pass/fail computation for a steady-state stage.
type EvidenceWindow struct {
	RunID            string    `json:"run_id"`
	Stage            StageName `json:"stage"`
	Cycle            int       `json:"cycle"`
	CoveragePPM      int64     `json:"coverage_ppm"`
	Samples          int64     `json:"samples"`
	RangeMilliKelvin int64     `json:"range_milli_kelvin"`
	DriftPPM         int64     `json:"drift_ppm"`
	Passed           bool      `json:"passed"`
}

// Anomaly captures a frozen-step fact and its propagation basis.
type Anomaly struct {
	ID      string `json:"id"`
	RunID   string `json:"run_id"`
	Summary string `json:"summary"`
	Basis   string `json:"basis"` // 传播依据
}

// RetestGeneration records an atomically created retest generation.
type RetestGeneration struct {
	RunID      string   `json:"run_id"`
	Generation int      `json:"generation"`
	Affected   []string `json:"affected"` // sorted, deduplicated affected sensor IDs
	Coverage   []string `json:"coverage,omitempty"`
}

// Review is an independent qualified sign-off on one evidence digest.
type Review struct {
	ID        string `json:"id"`
	RunID     string `json:"run_id"`
	Reviewer  string `json:"reviewer"`
	Qualified bool   `json:"qualified"`
	Digest    string `json:"digest"` // 证据摘要
}

// VerdictType enumerates the three mutually exclusive final outcomes.
type VerdictType string

const (
	VerdictRelease   VerdictType = "release"   // 出舱放行
	VerdictIsolate   VerdictType = "isolate"   // 继续隔离
	VerdictTerminate VerdictType = "terminate" // 试验终止
)

// FinalVerdict is the single, non-replaceable outcome of a run.
type FinalVerdict struct {
	RunID      string      `json:"run_id"`
	Type       VerdictType `json:"type"`
	Credential string      `json:"credential"` // 唯一凭据
	EventSeq   int64       `json:"event_seq"`
}

// IdempotencyRecord binds a key to a canonical request digest and response.
type IdempotencyRecord struct {
	Key           string `json:"key"`
	RequestDigest string `json:"request_digest"`
	Status        int    `json:"status"`
	Response      []byte `json:"response,omitempty"`
	EventSeq      int64  `json:"event_seq"`
}

// Equipment describes a leased physical device and its calibration window.
type Equipment struct {
	ID                    string        `json:"id"`
	Type                  EquipmentType `json:"type"`
	CalibrationSummary    string        `json:"calibration_summary"`
	CalibrationValidUntil int64         `json:"calibration_valid_until"`
}

// Baseline captures the pre-flight installation evidence that must all pass
// before evacuation may begin.
type Baseline struct {
	RunID                  string           `json:"run_id"`
	InstallCheckOK         bool             `json:"install_check_ok"`
	DoorClosed             bool             `json:"door_closed"`
	InitialPressureMilliPa int64            `json:"initial_pressure_milli_pa"`
	SensorZeros            map[string]int64 `json:"sensor_zeros"` // sensor ID -> zero milliKelvin
	Completed              bool             `json:"completed"`
	CompletedAtMillis      int64            `json:"completed_at_millis,omitempty"`
}

// BaselineRequest is the JSON payload for POST /v1/runs/{id}/baseline.
type BaselineRequest struct {
	InstallCheckOK         bool             `json:"install_check_ok"`
	DoorClosed             bool             `json:"door_closed"`
	InitialPressureMilliPa int64            `json:"initial_pressure_milli_pa"`
	SensorZeros            map[string]int64 `json:"sensor_zeros"`
}

// Scripted acquisition failure modes produced by the equipment adapter.
const (
	FailureTimeout      = "timeout"
	FailureDisconnect   = "disconnect"
	FailureExpiredCal   = "expired_cal"
	FailureFormat       = "format_error"
	FailureExpiredToken = "expired_token"
)

// AcquireOutcome is the result of one scripted acquisition attempt. On failure
// the FailureType identifies the deterministic fault; on success the reading
// fields carry the observed values.
type AcquireOutcome struct {
	Success                bool   `json:"success"`
	FailureType            string `json:"failure_type,omitempty"`
	PayloadSummary         string `json:"payload_summary,omitempty"`
	TemperatureMilliKelvin int64  `json:"temperature_milli_kelvin,omitempty"`
	PressureMilliPa        int64  `json:"pressure_milli_pa,omitempty"`
}
