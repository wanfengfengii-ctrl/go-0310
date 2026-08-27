package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"thermal-vacuum-test-gate/internal/domain"
)

// AcquireLease inserts a lease. The unique equipment_id column makes two
// concurrent acquisitions for the same equipment fail atomically at the
// database level.
func (d *DB) AcquireLease(ctx context.Context, l domain.Lease) error {
	_, err := d.exec.ExecContext(ctx,
		`INSERT INTO leases (id, equipment_id, holder, token, valid_until_millis) VALUES (?, ?, ?, ?, ?)`,
		l.ID, l.EquipmentID, l.Holder, l.Token, l.ValidUntilMillis)
	return err
}

// GetLease loads the current lease for an equipment id.
func (d *DB) GetLease(ctx context.Context, equipmentID string) (domain.Lease, error) {
	var l domain.Lease
	err := d.exec.QueryRowContext(ctx,
		`SELECT id, equipment_id, holder, token, valid_until_millis FROM leases WHERE equipment_id = ?`,
		equipmentID).
		Scan(&l.ID, &l.EquipmentID, &l.Holder, &l.Token, &l.ValidUntilMillis)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Lease{}, domain.NewError(domain.CodeLeaseConflict, "no lease held")
	}
	if err != nil {
		return domain.Lease{}, err
	}
	return l, nil
}

// RenewLease extends a lease only when the token still matches.
func (d *DB) RenewLease(ctx context.Context, equipmentID, token string, validUntilMillis int64) error {
	res, err := d.exec.ExecContext(ctx,
		`UPDATE leases SET valid_until_millis = ? WHERE equipment_id = ? AND token = ?`,
		validUntilMillis, equipmentID, token)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.NewError(domain.CodeLeaseExpired, "lease expired or token mismatch")
	}
	return nil
}

// ReleaseLease deletes a lease only when the token still matches, so a stale
// holder cannot release a lease it no longer owns.
func (d *DB) ReleaseLease(ctx context.Context, equipmentID, token string) error {
	res, err := d.exec.ExecContext(ctx,
		`DELETE FROM leases WHERE equipment_id = ? AND token = ?`, equipmentID, token)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.NewError(domain.CodeLeaseExpired, "lease expired or token mismatch")
	}
	return nil
}

// ExpireLeasesBefore deletes leases whose validity has lapsed and returns the
// number removed. It is used by recovery to invalidate stale leases.
func (d *DB) ExpireLeasesBefore(ctx context.Context, atMillis int64) (int64, error) {
	res, err := d.exec.ExecContext(ctx,
		`DELETE FROM leases WHERE valid_until_millis < ?`, atMillis)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
