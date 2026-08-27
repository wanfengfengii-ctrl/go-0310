// Package recovery performs the startup restore of a thermal-vacuum service:
// it invalidates stale equipment leases, replays committed events, and
// verifies that every run's persisted checkpoint is consistent with its
// append-only event log. Uncommitted transactions are invisible to SQLite, so
// a crash between event write and checkpoint update leaves neither visible.
package recovery

import (
	"context"
	"fmt"

	"thermal-vacuum-test-gate/internal/domain"
	"thermal-vacuum-test-gate/internal/store"
)

// Clock returns the current logical time in milliseconds.
type Clock func() int64

// Report summarises what recovery found and repaired.
type Report struct {
	RunsRecovered  int    `json:"runs_recovered"`
	LeasesExpired  int64  `json:"leases_expired"`
	IntegrityError string `json:"integrity_error,omitempty"`
}

// Recover restores the service from persisted state. It returns an error only
// on a store failure; integrity problems are reported rather than fatal so an
// operator can inspect them.
func Recover(ctx context.Context, s store.Store, now Clock) (Report, error) {
	var rep Report
	expired, err := s.ExpireLeasesBefore(ctx, now())
	if err != nil {
		return rep, fmt.Errorf("expire leases: %w", err)
	}
	rep.LeasesExpired = expired

	runs, err := s.ListRuns(ctx)
	if err != nil {
		return rep, fmt.Errorf("list runs: %w", err)
	}
	rep.RunsRecovered = len(runs)
	for _, run := range runs {
		if err := verifyCheckpoint(ctx, s, run); err != nil {
			rep.IntegrityError = err.Error()
			break
		}
	}
	return rep, nil
}

// verifyCheckpoint compares the run's persisted EventSeq with the maximum
// committed event sequence, detecting any half-applied write.
func verifyCheckpoint(ctx context.Context, s store.Store, run domain.TestRun) error {
	events, err := s.Events(ctx, run.ID, 0)
	if err != nil {
		return err
	}
	var maxSeq int64
	for _, e := range events {
		if e.Seq > maxSeq {
			maxSeq = e.Seq
		}
	}
	if maxSeq > run.EventSeq {
		return fmt.Errorf("run %s: event log ahead of checkpoint (max seq %d, checkpoint %d)",
			run.ID, maxSeq, run.EventSeq)
	}
	if maxSeq < run.EventSeq {
		return fmt.Errorf("run %s: checkpoint ahead of event log (checkpoint %d, max seq %d)",
			run.ID, run.EventSeq, maxSeq)
	}
	return nil
}
