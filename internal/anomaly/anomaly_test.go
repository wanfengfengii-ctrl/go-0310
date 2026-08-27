package anomaly

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"thermal-vacuum-test-gate/internal/domain"
	"thermal-vacuum-test-gate/internal/store/sqlite"
)

func TestAffectedClosureDeterministic(t *testing.T) {
	plan := domain.TestPlan{
		Sensors: []domain.SensorSpec{
			{ID: "s1", Group: "g1", CollectorID: "c1"},
			{ID: "s2", Group: "g1", CollectorID: "c1"},
			{ID: "s3", Group: "g2", CollectorID: "c1"},
			{ID: "s4", Group: "g2", CollectorID: "c2"},
		},
	}
	got := AffectedClosure(plan, "s1")
	want := []string{"s1", "s2", "s3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("closure = %v; want %v", got, want)
	}
}

func newStore(t *testing.T) *sqlite.DB {
	t.Helper()
	db, err := sqlite.Open("")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func seededRun(t *testing.T, db *sqlite.DB) {
	t.Helper()
	plan := domain.TestPlan{
		ID: "plan-1", SpecimenID: "spec-1", CalibrationSummary: "cal", Cycles: 1,
		Sensors: []domain.SensorSpec{{ID: "s1", Group: "g1", CollectorID: "c1"}},
		Stages: []domain.StageSpec{
			{Name: domain.StageEvacuate, Sequence: 1},
			{Name: domain.StageColdSoak, Sequence: 2, Dependencies: []domain.StageName{domain.StageEvacuate}},
			{Name: domain.StageHotSoak, Sequence: 3, Dependencies: []domain.StageName{domain.StageColdSoak}},
		},
	}
	if err := db.SavePlan(context.Background(), plan); err != nil {
		t.Fatalf("save plan: %v", err)
	}
	if err := db.CreateRun(context.Background(), domain.TestRun{
		ID: "run-1", PlanID: "plan-1", PlanVersion: 1, Generation: 1,
		Stage: domain.StageColdSoak, CurrentCycle: 1, EventSeq: 1,
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
}

func TestConcurrentAnomalySingleGeneration(t *testing.T) {
	db := newStore(t)
	seededRun(t, db)
	var now int64 = 5000
	g := NewGenerator(db, func() int64 { return now })

	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := g.CreateAnomaly(context.Background(), "run-1", "s1", "loss", "disconnect")
			results <- err
		}()
	}
	wg.Wait()
	close(results)

	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !domain.Is(err, domain.CodeGenerationConflict) && !domain.Is(err, domain.CodeRunFrozen) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if success != 1 {
		t.Fatalf("success=%d; want exactly 1", success)
	}
	run, _ := db.GetRun(context.Background(), "run-1")
	if run.Generation != 2 {
		t.Fatalf("generation = %d; want 2", run.Generation)
	}
}

func TestDuplicateReviewRejected(t *testing.T) {
	db := newStore(t)
	seededRun(t, db)
	a := NewArbiter(db, func() int64 { return 5000 })

	if _, err := a.AddReview(context.Background(), "run-1", "alice", true, "digest-1"); err != nil {
		t.Fatalf("first review: %v", err)
	}
	if _, err := a.AddReview(context.Background(), "run-1", "alice", true, "digest-1"); !domain.Is(err, domain.CodeDuplicateReview) {
		t.Fatalf("expected duplicate review, got %v", err)
	}
}

func TestUnqualifiedReviewRejected(t *testing.T) {
	db := newStore(t)
	seededRun(t, db)
	a := NewArbiter(db, func() int64 { return 5000 })
	if _, err := a.AddReview(context.Background(), "run-1", "bob", false, "digest-1"); !domain.Is(err, domain.CodeNotQualified) {
		t.Fatalf("expected not qualified, got %v", err)
	}
}

func TestConcurrentVerdictSingleWinner(t *testing.T) {
	db := newStore(t)
	seededRun(t, db)
	// Mark the run complete with all cycles closed.
	run, _ := db.GetRun(context.Background(), "run-1")
	run.Completed = true
	run.CompletedCycles = 1
	if err := db.UpdateRun(context.Background(), run); err != nil {
		t.Fatalf("update run: %v", err)
	}
	a := NewArbiter(db, func() int64 { return 5000 })
	if _, err := a.AddReview(context.Background(), "run-1", "alice", true, "digest-1"); err != nil {
		t.Fatalf("review alice: %v", err)
	}
	if _, err := a.AddReview(context.Background(), "run-1", "bob", true, "digest-1"); err != nil {
		t.Fatalf("review bob: %v", err)
	}

	types := []domain.VerdictType{domain.VerdictRelease, domain.VerdictIsolate, domain.VerdictTerminate}
	results := make(chan error, len(types))
	var wg sync.WaitGroup
	for _, vt := range types {
		wg.Add(1)
		go func(vt domain.VerdictType) {
			defer wg.Done()
			_, err := a.CommitVerdict(context.Background(), "run-1", vt)
			results <- err
		}(vt)
	}
	wg.Wait()
	close(results)

	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !domain.Is(err, domain.CodeVerdictConflict) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if success != 1 {
		t.Fatalf("success=%d; want exactly 1", success)
	}
	v, err := a.Verdict(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("get verdict: %v", err)
	}
	if v.Credential == "" {
		t.Fatalf("expected a non-empty credential")
	}
}
