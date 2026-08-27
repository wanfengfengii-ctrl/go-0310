package engine

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"thermal-vacuum-test-gate/internal/domain"
	"thermal-vacuum-test-gate/internal/lease"
	"thermal-vacuum-test-gate/internal/store"
	"thermal-vacuum-test-gate/internal/store/sqlite"
)

type synchronizedCallReadStore struct {
	store.Store

	mu      sync.Mutex
	reads   int
	release chan struct{}
}

func (s *synchronizedCallReadStore) Calls(ctx context.Context, equipmentID string) ([]domain.MeasurementCall, error) {
	calls, err := s.Store.Calls(ctx, equipmentID)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.reads++
	read := s.reads
	if read == 2 {
		close(s.release)
	}
	s.mu.Unlock()

	if read <= 2 {
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return calls, nil
}

func TestModel_ConcurrentCollectAttemptsPersistUniqueMonotonic(t *testing.T) {
	tests := []struct {
		name       string
		afterCalls int
		want       []int
	}{
		{name: "two concurrent failures", want: []int{1, 2}},
		{name: "next failure continues sequence", afterCalls: 1, want: []int{1, 2, 3}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, err := sqlite.Open("")
			if err != nil {
				t.Fatalf("open database: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			if err := db.Migrate(context.Background()); err != nil {
				t.Fatalf("migrate database: %v", err)
			}

			wrapped := &synchronizedCallReadStore{Store: db, release: make(chan struct{})}
			e := New(wrapped, nil)
			acquirer := lease.NewAcquirer()
			acquirer.Script("collector-1", domain.AcquireOutcome{
				Success: false, FailureType: domain.FailureTimeout,
			})
			e.SetAcquirer(acquirer)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			type result struct {
				call        domain.MeasurementCall
				measurement domain.Measurement
				err         error
			}
			results := make(chan result, 2)
			start := make(chan struct{})
			for i := 0; i < 2; i++ {
				go func() {
					<-start
					call, measurement, err := e.CollectMeasurement(ctx, "run-1", domain.MeasurementRequest{
						CollectorID: "collector-1",
					})
					results <- result{call: call, measurement: measurement, err: err}
				}()
			}
			close(start)

			observed := make([]int, 0, len(tt.want))
			for i := 0; i < 2; i++ {
				got := <-results
				if !domain.Is(got.err, domain.CodeConflict) {
					t.Fatalf("concurrent collect error = %v; want conflict", got.err)
				}
				if got.measurement.ID != "" || got.measurement.Valid {
					t.Fatalf("failed collect produced measurement: %+v", got.measurement)
				}
				observed = append(observed, got.call.Attempt)
			}

			for i := 0; i < tt.afterCalls; i++ {
				call, measurement, err := e.CollectMeasurement(ctx, "run-1", domain.MeasurementRequest{
					CollectorID: "collector-1",
				})
				if !domain.Is(err, domain.CodeConflict) {
					t.Fatalf("subsequent collect error = %v; want conflict", err)
				}
				if measurement.ID != "" || measurement.Valid {
					t.Fatalf("failed subsequent collect produced measurement: %+v", measurement)
				}
				observed = append(observed, call.Attempt)
			}

			sort.Ints(observed)
			for i, want := range tt.want {
				if observed[i] != want {
					t.Fatalf("returned attempts = %v; want %v", observed, tt.want)
				}
			}

			calls, err := db.Calls(ctx, "collector-1")
			if err != nil {
				t.Fatalf("load persisted calls: %v", err)
			}
			if len(calls) != len(tt.want) {
				t.Fatalf("persisted call count = %d; want %d", len(calls), len(tt.want))
			}
			for i, want := range tt.want {
				if calls[i].Attempt != want {
					t.Fatalf("persisted attempts = %v; want %v", observed, tt.want)
				}
				if calls[i].Success || calls[i].FailureType != domain.FailureTimeout {
					t.Fatalf("persisted call %d = %+v; want timeout failure", i, calls[i])
				}
			}

			measurements, err := db.AllMeasurements(ctx, "run-1")
			if err != nil {
				t.Fatalf("load measurements: %v", err)
			}
			if len(measurements) != 0 {
				t.Fatalf("failed calls persisted %d measurements; want none", len(measurements))
			}
		})
	}
}
