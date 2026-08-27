package anomaly

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"thermal-vacuum-test-gate/internal/domain"
	"thermal-vacuum-test-gate/internal/store"
)

// Arbiter registers independent reviews and commits the single, non-replaceable
// final verdict for a run.
type Arbiter struct {
	store store.Store
	now   Clock
}

// NewArbiter builds a verdict arbiter.
func NewArbiter(s store.Store, now Clock) *Arbiter {
	return &Arbiter{store: s, now: now}
}

// AddReview registers an independent review. A review must be by a qualified
// reviewer against a non-empty evidence digest; the UNIQUE(run_id, reviewer)
// constraint rejects a duplicate reviewer review.
func (a *Arbiter) AddReview(ctx context.Context, runID, reviewer string, qualified bool, digest string) (domain.Review, error) {
	if !qualified {
		return domain.Review{}, domain.NewError(domain.CodeNotQualified, "reviewer is not qualified")
	}
	if digest == "" {
		return domain.Review{}, domain.NewError(domain.CodeInvalidRange, "evidence digest is required")
	}
	r := domain.Review{
		ID:        runID + "/" + reviewer,
		RunID:     runID,
		Reviewer:  reviewer,
		Qualified: qualified,
		Digest:    digest,
	}
	if err := a.store.AddReview(ctx, r); err != nil {
		return domain.Review{}, domain.NewError(domain.CodeDuplicateReview, "reviewer already signed")
	}
	return r, nil
}

// CommitVerdict atomically commits the final outcome. It requires all cycles to
// be closed, no incomplete retest (the run must not be frozen), the environment
// recovered (the run is completed) and two distinct qualified reviewers to have
// signed the same evidence digest. The verdict primary key guarantees a single
// winner among concurrent requests.
func (a *Arbiter) CommitVerdict(ctx context.Context, runID string, verdictType domain.VerdictType) (domain.FinalVerdict, error) {
	if verdictType != domain.VerdictRelease &&
		verdictType != domain.VerdictIsolate &&
		verdictType != domain.VerdictTerminate {
		return domain.FinalVerdict{}, domain.NewError(domain.CodeInvalidRange, "invalid verdict type")
	}
	var out domain.FinalVerdict
	err := a.store.WithTx(ctx, func(tx store.Tx) error {
		run, err := tx.GetRun(ctx, runID)
		if err != nil {
			return err
		}
		plan, err := tx.GetPlan(ctx, run.PlanID)
		if err != nil {
			return err
		}
		if !run.Completed {
			return domain.NewError(domain.CodeVerdictNotReady, "workflow not completed")
		}
		if run.Frozen {
			return domain.NewError(domain.CodeVerdictNotReady, "incomplete retest")
		}
		if run.CompletedCycles != plan.Cycles {
			return domain.NewError(domain.CodeVerdictNotReady, "cycles not closed")
		}
		reviews, err := tx.Reviews(ctx, runID)
		if err != nil {
			return err
		}
		if !validReviews(reviews) {
			return domain.NewError(domain.CodeVerdictNotReady, "two qualified reviewers required")
		}
		v := domain.FinalVerdict{
			RunID:      runID,
			Type:       verdictType,
			Credential: newCredential(),
			EventSeq:   run.EventSeq + 1,
		}
		if err := tx.CommitVerdict(ctx, v); err != nil {
			return domain.NewError(domain.CodeVerdictConflict, "verdict already committed")
		}
		run.EventSeq++
		if err := tx.UpdateRun(ctx, run); err != nil {
			return err
		}
		if err := tx.AppendEvent(ctx, domain.RunEvent{
			Seq:      run.EventSeq,
			RunID:    runID,
			Type:     "verdict.committed",
			Payload:  []byte(string(verdictType)),
			AtMillis: a.now(),
		}); err != nil {
			return err
		}
		out = v
		return nil
	})
	return out, err
}

// Verdict returns the committed verdict for a run.
func (a *Arbiter) Verdict(ctx context.Context, runID string) (domain.FinalVerdict, error) {
	return a.store.GetVerdict(ctx, runID)
}

// validReviews requires at least two distinct qualified reviewers who all
// signed the same non-empty evidence digest.
func validReviews(reviews []domain.Review) bool {
	digest := ""
	seen := map[string]bool{}
	distinct := 0
	for _, r := range reviews {
		if !r.Qualified || r.Digest == "" {
			return false
		}
		if digest == "" {
			digest = r.Digest
		}
		if r.Digest != digest {
			return false
		}
		if !seen[r.Reviewer] {
			seen[r.Reviewer] = true
			distinct++
		}
	}
	return distinct >= 2
}

func newCredential() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
