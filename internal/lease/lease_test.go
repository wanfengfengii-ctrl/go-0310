package lease

import (
	"context"
	"sync"
	"testing"

	"thermal-vacuum-test-gate/internal/domain"
	"thermal-vacuum-test-gate/internal/store/sqlite"
)

func newManager(t *testing.T) (*Manager, *sqlite.DB, func() int64) {
	t.Helper()
	db, err := sqlite.Open("")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var now int64 = 1000
	return NewManager(db, func() int64 { return now }), db, func() int64 { return now }
}

func TestConcurrentLeaseAcquireSingleWinner(t *testing.T) {
	m, _, _ := newManager(t)
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := m.Acquire(context.Background(), "chamber", "holder", 10_000)
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)

	success, conflict := 0, 0
	for err := range results {
		switch {
		case err == nil:
			success++
		case domain.Is(err, domain.CodeLeaseConflict):
			conflict++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d; want 1/1", success, conflict)
	}
}

func TestAcquireExpiredLeaseAllowed(t *testing.T) {
	m, db, _ := newManager(t)
	first, err := m.Acquire(context.Background(), "vac", "h", 100)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	// Expire the first lease.
	if _, err := db.ExpireLeasesBefore(context.Background(), 10_000); err != nil {
		t.Fatalf("expire: %v", err)
	}
	second, err := m.Acquire(context.Background(), "vac", "h2", 100)
	if err != nil {
		t.Fatalf("second acquire after expiry: %v", err)
	}
	if second.Token == first.Token {
		t.Fatalf("expected a fresh token")
	}
}

func TestRenewAndRelease(t *testing.T) {
	m, _, _ := newManager(t)
	l, err := m.Acquire(context.Background(), "th", "h", 1000)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	renewed, err := m.Renew(context.Background(), "th", l.Token, 2000)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if renewed.ValidUntilMillis <= l.ValidUntilMillis {
		t.Fatalf("renew did not extend validity")
	}
	if err := m.Release(context.Background(), "th", l.Token); err != nil {
		t.Fatalf("release: %v", err)
	}
	// A stale release must fail.
	if err := m.Release(context.Background(), "th", l.Token); !domain.Is(err, domain.CodeLeaseExpired) {
		t.Fatalf("expected lease expired on second release, got %v", err)
	}
}
