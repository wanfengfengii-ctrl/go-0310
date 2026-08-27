package engine_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"thermal-vacuum-test-gate/internal/domain"
	"thermal-vacuum-test-gate/internal/engine"
	"thermal-vacuum-test-gate/internal/lease"
	"thermal-vacuum-test-gate/internal/store/sqlite"
)

type twoArrivalAcquirer struct {
	next     engine.Acquirer
	arrivals atomic.Int32
	release  chan struct{}
	once     sync.Once
}

func (a *twoArrivalAcquirer) Collect(ctx context.Context, equipmentID string) domain.AcquireOutcome {
	if a.arrivals.Add(1) == 2 {
		a.once.Do(func() { close(a.release) })
	}
	select {
	case <-a.release:
	case <-time.After(500 * time.Millisecond):
		a.once.Do(func() { close(a.release) })
	}
	return a.next.Collect(ctx, equipmentID)
}

func TestModel_ConcurrentCollectConsumesScriptExactlyOnce(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, e *engine.Engine, db *sqlite.DB, acq *lease.Acquirer)
	}{
		{
			name: "same collector persists the scripted order under concurrency",
			run: func(t *testing.T, e *engine.Engine, db *sqlite.DB, acq *lease.Acquirer) {
				const requests = 12
				outcomes := make([]domain.AcquireOutcome, requests)
				for i := range outcomes {
					outcomes[i] = domain.AcquireOutcome{
						FailureType:    domain.FailureTimeout,
						PayloadSummary: fmt.Sprintf("script-%02d", i+1),
					}
				}
				acq.Script("collector-a", outcomes...)
				e.SetAcquirer(&twoArrivalAcquirer{next: acq, release: make(chan struct{})})

				start := make(chan struct{})
				errs := make(chan error, requests)
				var wg sync.WaitGroup
				for i := 0; i < requests; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-start
						_, _, err := e.CollectMeasurement(context.Background(), "unused-run", domain.MeasurementRequest{CollectorID: "collector-a"})
						if err == nil {
							errs <- fmt.Errorf("scripted failure unexpectedly succeeded")
						}
					}()
				}
				close(start)
				wg.Wait()
				close(errs)
				for err := range errs {
					t.Error(err)
				}

				calls, err := db.Calls(context.Background(), "collector-a")
				if err != nil {
					t.Fatalf("load calls: %v", err)
				}
				if len(calls) != requests {
					t.Fatalf("persisted calls = %d; want %d", len(calls), requests)
				}
				for i, call := range calls {
					if call.Attempt != i+1 || call.PayloadSummary != outcomes[i].PayloadSummary {
						t.Fatalf("call[%d] = (attempt %d, payload %q); want (attempt %d, payload %q)", i, call.Attempt, call.PayloadSummary, i+1, outcomes[i].PayloadSummary)
					}
				}
			},
		},
		{
			name: "different collectors consume independent queues",
			run: func(t *testing.T, e *engine.Engine, db *sqlite.DB, acq *lease.Acquirer) {
				for _, id := range []string{"collector-a", "collector-b"} {
					acq.Script(id,
						domain.AcquireOutcome{FailureType: domain.FailureTimeout, PayloadSummary: id + "-first"},
						domain.AcquireOutcome{FailureType: domain.FailureFormat, PayloadSummary: id + "-second"},
					)
				}
				var wg sync.WaitGroup
				for _, id := range []string{"collector-a", "collector-b"} {
					id := id
					wg.Add(1)
					go func() {
						defer wg.Done()
						for range 2 {
							_, _, _ = e.CollectMeasurement(context.Background(), "unused-run", domain.MeasurementRequest{CollectorID: id})
						}
					}()
				}
				wg.Wait()
				for _, id := range []string{"collector-a", "collector-b"} {
					calls, err := db.Calls(context.Background(), id)
					if err != nil {
						t.Fatalf("load %s calls: %v", id, err)
					}
					if len(calls) != 2 || calls[0].Attempt != 1 || calls[0].PayloadSummary != id+"-first" || calls[1].Attempt != 2 || calls[1].PayloadSummary != id+"-second" {
						t.Fatalf("%s calls = %#v; want its independent first and second outcomes", id, calls)
					}
				}
			},
		},
		{
			name: "exhausted script repeats its final outcome",
			run: func(t *testing.T, e *engine.Engine, db *sqlite.DB, acq *lease.Acquirer) {
				acq.Script("collector-a",
					domain.AcquireOutcome{FailureType: domain.FailureTimeout, PayloadSummary: "first"},
					domain.AcquireOutcome{FailureType: domain.FailureFormat, PayloadSummary: "last"},
				)
				for range 4 {
					_, _, _ = e.CollectMeasurement(context.Background(), "unused-run", domain.MeasurementRequest{CollectorID: "collector-a"})
				}
				calls, err := db.Calls(context.Background(), "collector-a")
				if err != nil {
					t.Fatalf("load calls: %v", err)
				}
				want := []string{"first", "last", "last", "last"}
				if len(calls) != len(want) {
					t.Fatalf("persisted calls = %d; want %d", len(calls), len(want))
				}
				for i, call := range calls {
					if call.Attempt != i+1 || call.PayloadSummary != want[i] {
						t.Fatalf("call[%d] = (attempt %d, payload %q); want (attempt %d, payload %q)", i, call.Attempt, call.PayloadSummary, i+1, want[i])
					}
				}
			},
		},
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
			acq := lease.NewAcquirer()
			e := engine.New(db, func() int64 { return 1 })
			e.SetAcquirer(acq)
			tt.run(t, e, db, acq)
		})
	}
}
