package postgres

import (
	"context"
	"fmt"

	"github.com/elug3/dupli1/auth/pkg/autherrors"
	"github.com/elug3/dupli1/shared/pkg/outbox"
)

// DeleteAndEnqueue removes the user and enqueues user.deleted in one
// transaction so a broker outage cannot drop the account without a durable
// event for profile cleanup.
func (r *UserRepository) DeleteAndEnqueue(ctx context.Context, userID, subject string, payload []byte) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete and enqueue: begin: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, userID)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete user: rows: %w", err)
	}
	if n == 0 {
		return autherrors.ErrUserNotFound
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO auth_outbox (aggregate_id, subject, payload)
		VALUES ($1, $2, $3)
	`, userID, subject, payload); err != nil {
		return fmt.Errorf("enqueue user.deleted: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete and enqueue: commit: %w", err)
	}
	return nil
}

func (r *UserRepository) ListPendingOutbox(ctx context.Context, limit int) ([]outbox.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, aggregate_id, subject, payload, created_at, attempts, last_error
		FROM auth_outbox
		WHERE published_at IS NULL
		ORDER BY id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []outbox.Message
	for rows.Next() {
		var m outbox.Message
		if err := rows.Scan(&m.ID, &m.AggregateID, &m.Subject, &m.Payload, &m.CreatedAt, &m.Attempts, &m.LastError); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *UserRepository) MarkOutboxPublished(ctx context.Context, id int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx, `
		UPDATE auth_outbox SET published_at = NOW(), last_error = '' WHERE id = $1
	`, id)
	return err
}

func (r *UserRepository) RecordOutboxAttempt(ctx context.Context, id int64, errMsg string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx, `
		UPDATE auth_outbox
		SET attempts = attempts + 1, last_error = $2
		WHERE id = $1
	`, id, errMsg)
	return err
}

var _ outbox.Store = (*UserRepository)(nil)
