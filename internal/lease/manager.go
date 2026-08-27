// Package lease manages time-bounded mutual-exclusion leases over physical
// equipment (chamber, thermostat, vacuum gauge, collector) and provides the
// scripted acquisition adapter that produces deterministic success or failure
// outcomes. It is the "设备租约与采集适配器" component.
package lease

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"thermal-vacuum-test-gate/internal/domain"
	"thermal-vacuum-test-gate/internal/store"
)

// Clock returns the current logical time in milliseconds.
type Clock func() int64

// Manager coordinates equipment lease acquisition, renewal and release.
type Manager struct {
	store store.Store
	now   Clock
}

// NewManager builds a lease manager.
func NewManager(s store.Store, now Clock) *Manager {
	return &Manager{store: s, now: now}
}

// Acquire takes an exclusive, time-bounded lease on an equipment id. Expired
// leases are invalidated in the same transaction, and the unique equipment
// constraint guarantees exactly one winner among concurrent acquirers.
func (m *Manager) Acquire(ctx context.Context, equipmentID, holder string, ttlMillis int64) (domain.Lease, error) {
	now := m.now()
	if ttlMillis <= 0 {
		return domain.Lease{}, domain.NewError(domain.CodeInvalidRange, "lease ttl must be positive")
	}
	var out domain.Lease
	err := m.store.WithTx(ctx, func(tx store.Tx) error {
		if _, err := tx.ExpireLeasesBefore(ctx, now); err != nil {
			return err
		}
		l := domain.Lease{
			ID:               newToken(),
			EquipmentID:      equipmentID,
			Holder:           holder,
			Token:            newToken(),
			ValidUntilMillis: now + ttlMillis,
		}
		if err := tx.AcquireLease(ctx, l); err != nil {
			return domain.NewError(domain.CodeLeaseConflict, "equipment already leased")
		}
		out = l
		return nil
	})
	return out, err
}

// Renew extends a lease's validity when the caller still holds the token.
func (m *Manager) Renew(ctx context.Context, equipmentID, token string, ttlMillis int64) (domain.Lease, error) {
	now := m.now()
	var out domain.Lease
	err := m.store.WithTx(ctx, func(tx store.Tx) error {
		lease, err := tx.GetLease(ctx, equipmentID)
		if err != nil {
			return domain.NewError(domain.CodeLeaseExpired, "no active lease")
		}
		if lease.Token != token || lease.ValidUntilMillis < now {
			return domain.NewError(domain.CodeLeaseExpired, "lease expired or token mismatch")
		}
		newUntil := now + ttlMillis
		if err := tx.RenewLease(ctx, equipmentID, token, newUntil); err != nil {
			return err
		}
		lease.ValidUntilMillis = newUntil
		out = lease
		return nil
	})
	return out, err
}

// Release frees a lease when the caller still holds the token.
func (m *Manager) Release(ctx context.Context, equipmentID, token string) error {
	return m.store.ReleaseLease(ctx, equipmentID, token)
}

// Get returns the current lease for an equipment id.
func (m *Manager) Get(ctx context.Context, equipmentID string) (domain.Lease, error) {
	return m.store.GetLease(ctx, equipmentID)
}

// ExpireBefore invalidates all leases whose validity has lapsed at the given
// logical time. It is the recovery entry point for stale-lease invalidation.
func (m *Manager) ExpireBefore(ctx context.Context, atMillis int64) (int64, error) {
	return m.store.ExpireLeasesBefore(ctx, atMillis)
}

func newToken() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
