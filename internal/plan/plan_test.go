package plan

import (
	"context"
	"testing"

	"thermal-vacuum-test-gate/internal/domain"
)

type memStore struct {
	plans map[string]domain.TestPlan
}

func newMemStore() *memStore { return &memStore{plans: map[string]domain.TestPlan{}} }

func (m *memStore) SavePlan(_ context.Context, p domain.TestPlan) error {
	m.plans[p.ID] = p
	return nil
}
func (m *memStore) GetPlan(_ context.Context, id string) (domain.TestPlan, error) {
	p, ok := m.plans[id]
	if !ok {
		return domain.TestPlan{}, domain.NewError(domain.CodePlanNotFound, "plan not found")
	}
	return p, nil
}

func validPlan() domain.TestPlan {
	return domain.TestPlan{
		ID:                 "plan-1",
		SpecimenID:         "spec-1",
		CalibrationSummary: "cal-2026",
		Cycles:             2,
		Sensors: []domain.SensorSpec{
			{ID: "s1", Group: "g1", CollectorID: "c1"},
			{ID: "s2", Group: "g1", CollectorID: "c1"},
		},
		Stages: []domain.StageSpec{
			{Name: domain.StageEvacuate, Sequence: 1},
			{Name: domain.StageColdSoak, Sequence: 2, Dependencies: []domain.StageName{domain.StageEvacuate}},
			{Name: domain.StageHotSoak, Sequence: 3, Dependencies: []domain.StageName{domain.StageColdSoak}},
		},
	}
}

func TestLockPlanSuccess(t *testing.T) {
	c := NewCatalog(newMemStore())
	locked, err := c.LockPlan(context.Background(), validPlan())
	if err != nil {
		t.Fatalf("LockPlan returned error: %v", err)
	}
	if locked.Version != 1 {
		t.Fatalf("locked version = %d; want 1", locked.Version)
	}
}

func TestLockPlanRejectsDuplicateSensor(t *testing.T) {
	p := validPlan()
	p.Sensors = append(p.Sensors, domain.SensorSpec{ID: "s1", Group: "g2", CollectorID: "c2"})
	_, err := NewCatalog(newMemStore()).LockPlan(context.Background(), p)
	if !domain.Is(err, domain.CodeInvalidRange) {
		t.Fatalf("expected invalid range error, got %v", err)
	}
}

func TestLockPlanRejectsDependencyCycle(t *testing.T) {
	p := validPlan()
	p.Stages = []domain.StageSpec{
		{Name: domain.StageEvacuate, Sequence: 1, Dependencies: []domain.StageName{domain.StageColdSoak}},
		{Name: domain.StageColdSoak, Sequence: 2, Dependencies: []domain.StageName{domain.StageEvacuate}},
	}
	_, err := NewCatalog(newMemStore()).LockPlan(context.Background(), p)
	if !domain.Is(err, domain.CodeInvalidRange) {
		t.Fatalf("expected invalid range error, got %v", err)
	}
}

func TestLockPlanRejectsMissingCalibration(t *testing.T) {
	p := validPlan()
	p.CalibrationSummary = ""
	_, err := NewCatalog(newMemStore()).LockPlan(context.Background(), p)
	if !domain.Is(err, domain.CodeInvalidRange) {
		t.Fatalf("expected invalid range error, got %v", err)
	}
}

func TestLockPlanRejectsStaleCalibration(t *testing.T) {
	p := validPlan()
	p.CalibrationValidUntilMillis = 1000
	c := NewCatalog(newMemStore()).SetClock(func() int64 { return 2000 })
	_, err := c.LockPlan(context.Background(), p)
	if !domain.Is(err, domain.CodeInvalidRange) {
		t.Fatalf("expected invalid range error for stale calibration, got %v", err)
	}
}

func TestLockPlanRejectsUncoveredStage(t *testing.T) {
	p := validPlan()
	p.Stages = append(p.Stages, domain.StageSpec{Name: domain.StageName("bogus"), Sequence: 4})
	_, err := NewCatalog(newMemStore()).LockPlan(context.Background(), p)
	if !domain.Is(err, domain.CodeInvalidRange) {
		t.Fatalf("expected invalid range error for uncovered stage, got %v", err)
	}
}

func TestLockPlanRejectsDanglingDependency(t *testing.T) {
	p := validPlan()
	p.Stages[1].Dependencies = []domain.StageName{domain.StageName("missing_stage")}
	_, err := NewCatalog(newMemStore()).LockPlan(context.Background(), p)
	if !domain.Is(err, domain.CodeInvalidRange) {
		t.Fatalf("expected invalid range error for dangling dependency, got %v", err)
	}
}
