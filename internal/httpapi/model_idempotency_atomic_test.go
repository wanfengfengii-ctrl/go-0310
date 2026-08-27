package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"thermal-vacuum-test-gate/internal/domain"
	"thermal-vacuum-test-gate/internal/store/sqlite"
)

type modelIdempotencyStore struct {
	*sqlite.DB
	firstLookup  chan struct{}
	secondLookup chan struct{}
	releaseFirst chan struct{}
	onceFirst    sync.Once
	onceSecond   sync.Once
	mu           sync.Mutex
	lookups      int
}

func (s *modelIdempotencyStore) GetIdempotency(ctx context.Context, key string) (domain.IdempotencyRecord, error) {
	s.mu.Lock()
	s.lookups++
	lookup := s.lookups
	s.mu.Unlock()

	if lookup == 1 {
		s.onceFirst.Do(func() { close(s.firstLookup) })
		select {
		case <-s.releaseFirst:
		case <-ctx.Done():
			return domain.IdempotencyRecord{}, ctx.Err()
		}
	} else if lookup == 2 {
		rec, err := s.DB.GetIdempotency(ctx, key)
		s.onceSecond.Do(func() { close(s.secondLookup) })
		return rec, err
	}
	return s.DB.GetIdempotency(ctx, key)
}

func TestModel_IdempotencyAtomicBoundary(t *testing.T) {
	cases := []struct {
		name             string
		concurrent       bool
		keys             [2]string
		bodies           [2]string
		wantStatuses     [2]int
		wantRuns         int
		wantSameResponse bool
	}{
		{
			name:             "concurrent same key and content executes one write",
			concurrent:       true,
			keys:             [2]string{"retry-key", "retry-key"},
			bodies:           [2]string{`{"plan_id":"plan-1"}`, `{"plan_id":"plan-1"}`},
			wantStatuses:     [2]int{http.StatusCreated, http.StatusCreated},
			wantRuns:         1,
			wantSameResponse: true,
		},
		{
			name:         "concurrent same key and different content conflicts without a second write",
			concurrent:   true,
			keys:         [2]string{"conflict-key", "conflict-key"},
			bodies:       [2]string{`{"plan_id":"plan-1"}`, `{"plan_id":"plan-1","run_id":"requested-run"}`},
			wantStatuses: [2]int{http.StatusCreated, http.StatusConflict},
			wantRuns:     1,
		},
		{
			name:         "concurrent different keys execute independent writes",
			concurrent:   true,
			keys:         [2]string{"independent-a", "independent-b"},
			bodies:       [2]string{`{"plan_id":"plan-1"}`, `{"plan_id":"plan-1"}`},
			wantStatuses: [2]int{http.StatusCreated, http.StatusCreated},
			wantRuns:     2,
		},
		{
			name:             "sequential replay preserves the original response",
			keys:             [2]string{"replay-key", "replay-key"},
			bodies:           [2]string{`{"plan_id":"plan-1"}`, `{"plan_id":"plan-1"}`},
			wantStatuses:     [2]int{http.StatusCreated, http.StatusCreated},
			wantRuns:         1,
			wantSameResponse: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, err := sqlite.Open("")
			if err != nil {
				t.Fatalf("open database: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			if err := db.Migrate(context.Background()); err != nil {
				t.Fatalf("migrate database: %v", err)
			}
			if err := db.SavePlan(context.Background(), domain.TestPlan{ID: "plan-1", Version: 1}); err != nil {
				t.Fatalf("save plan: %v", err)
			}

			store := &modelIdempotencyStore{
				DB:           db,
				firstLookup:  make(chan struct{}),
				secondLookup: make(chan struct{}),
				releaseFirst: make(chan struct{}),
			}
			handler := NewServer(store, func() int64 { return 1_000 }).Handler()
			responses := [2]*httptest.ResponseRecorder{}
			request := func(i int) {
				req := httptest.NewRequest(http.MethodPost, "/v1/runs", strings.NewReader(tc.bodies[i]))
				req.Header.Set("Idempotency-Key", tc.keys[i])
				responses[i] = httptest.NewRecorder()
				handler.ServeHTTP(responses[i], req)
			}

			if tc.concurrent {
				done := make(chan int, 2)
				go func() { request(0); done <- 0 }()
				select {
				case <-store.firstLookup:
				case <-time.After(2 * time.Second):
					t.Fatal("first request did not reach idempotency lookup")
				}
				go func() { request(1); done <- 1 }()

				select {
				case <-store.secondLookup:
				case <-time.After(500 * time.Millisecond):
				}
				close(store.releaseFirst)
				for range 2 {
					select {
					case <-done:
					case <-time.After(2 * time.Second):
						t.Fatal("concurrent request did not complete")
					}
				}
			} else {
				close(store.releaseFirst)
				request(0)
				request(1)
			}

			gotStatuses := []int{responses[0].Code, responses[1].Code}
			wantStatuses := []int{tc.wantStatuses[0], tc.wantStatuses[1]}
			sort.Ints(gotStatuses)
			sort.Ints(wantStatuses)
			if gotStatuses[0] != wantStatuses[0] || gotStatuses[1] != wantStatuses[1] {
				t.Fatalf("statuses = %v; want %v", gotStatuses, wantStatuses)
			}

			runs, err := db.ListRuns(context.Background())
			if err != nil {
				t.Fatalf("list recovered runs: %v", err)
			}
			if len(runs) != tc.wantRuns {
				t.Fatalf("persisted runs = %d; want %d", len(runs), tc.wantRuns)
			}
			if tc.wantSameResponse {
				first, _ := io.ReadAll(responses[0].Body)
				second, _ := io.ReadAll(responses[1].Body)
				if string(first) != string(second) {
					t.Fatalf("replayed responses differ: %s != %s", first, second)
				}
			}
		})
	}
}
