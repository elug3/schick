package postgres_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/elug3/dupli1/auth/pkg/autherrors"
	"github.com/elug3/dupli1/auth/pkg/bootstrap"
	"github.com/elug3/dupli1/auth/pkg/domain"
	"github.com/elug3/dupli1/auth/pkg/infra/postgres"
	"github.com/elug3/dupli1/shared/pkg/events"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

var (
	testDSN string
	repo    *postgres.UserRepository
)

func TestMain(m *testing.M) {
	testDSN = os.Getenv("POSTGRES_URL")
	if testDSN == "" {
		os.Exit(m.Run())
	}

	db, err := sql.Open("postgres", testDSN)
	if err != nil {
		panic("open postgres: " + err.Error())
	}
	if err := db.Ping(); err != nil {
		panic("ping postgres: " + err.Error())
	}
	// test harness: no HTTP request context
	if err := bootstrap.MigrateSchema(context.Background(), db); err != nil {
		panic("migrate schema: " + err.Error())
	}

	repo = postgres.NewUserRepository(db)

	code := m.Run()

	_, _ = db.Exec("DROP TABLE IF EXISTS users")
	db.Close()

	os.Exit(code)
}

func requirePostgres(t *testing.T) {
	t.Helper()
	if testDSN == "" {
		t.Skip("POSTGRES_URL not set; skipping postgres integration test")
	}
}

func newTestUser(t *testing.T) *domain.User {
	t.Helper()
	email := "test+" + uuid.New().String() + "@example.com"
	u, err := domain.NewUser(uuid.New().String(), email, "hunter2", domain.AccountTypeCustomer)
	if err != nil {
		t.Fatalf("NewUser: %v", err)
	}
	return u
}

func TestSave_And_FindByEmail(t *testing.T) {
	requirePostgres(t)
	ctx := t.Context()
	u := newTestUser(t)

	if err := repo.Save(ctx, u); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := repo.FindByEmail(ctx, u.Email)
	if err != nil {
		t.Fatalf("FindByEmail: %v", err)
	}
	if got == nil {
		t.Fatal("expected user, got nil")
	}
	if got.ID != u.ID {
		t.Errorf("ID: got %v, want %v", got.ID, u.ID)
	}
	if got.Email != u.Email {
		t.Errorf("Email: got %q, want %q", got.Email, u.Email)
	}
	if got.AccountType != u.AccountType {
		t.Errorf("AccountType: got %q, want %q", got.AccountType, u.AccountType)
	}
}

func TestFindByEmail_NotFound(t *testing.T) {
	requirePostgres(t)
	ctx := t.Context()
	got, err := repo.FindByEmail(ctx, "nobody@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestSave_And_FindByID(t *testing.T) {
	requirePostgres(t)
	ctx := t.Context()
	u := newTestUser(t)

	if err := repo.Save(ctx, u); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := repo.FindByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if got == nil {
		t.Fatal("expected user, got nil")
	}
	if got.ID != u.ID {
		t.Errorf("ID: got %v, want %v", got.ID, u.ID)
	}
}

func TestFindByID_NotFound(t *testing.T) {
	requirePostgres(t)
	ctx := t.Context()
	got, err := repo.FindByID(ctx, uuid.New().String())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestSave_UpdateExisting(t *testing.T) {
	requirePostgres(t)
	ctx := t.Context()
	u := newTestUser(t)

	if err := repo.Save(ctx, u); err != nil {
		t.Fatalf("initial Save: %v", err)
	}

	newEmail := "updated+" + uuid.New().String() + "@example.com"
	u.Email = newEmail
	if err := repo.Save(ctx, u); err != nil {
		t.Fatalf("update Save: %v", err)
	}

	got, err := repo.FindByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if got.Email != newEmail {
		t.Errorf("Email: got %q, want %q", got.Email, newEmail)
	}
}

func TestSave_DuplicateEmail(t *testing.T) {
	requirePostgres(t)
	ctx := t.Context()
	u1 := newTestUser(t)
	if err := repo.Save(ctx, u1); err != nil {
		t.Fatalf("Save u1: %v", err)
	}

	u2, err := domain.NewUser(uuid.New().String(), u1.Email, "password", domain.AccountTypeCustomer, "customer")
	if err != nil {
		t.Fatalf("NewUser: %v", err)
	}

	err = repo.Save(ctx, u2)
	if err != autherrors.ErrUserAlreadyExists {
		t.Fatalf("expected ErrUserAlreadyExists, got %v", err)
	}
}

func TestDelete(t *testing.T) {
	requirePostgres(t)
	ctx := t.Context()
	u := newTestUser(t)

	if err := repo.Save(ctx, u); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := repo.Delete(ctx, u.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	got, err := repo.FindByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("FindByID after delete: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil after delete, got %+v", got)
	}
}

func TestDelete_NonExistent(t *testing.T) {
	requirePostgres(t)
	ctx := t.Context()
	if err := repo.Delete(ctx, uuid.New().String()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// DeleteAndEnqueue must remove the user and durably record user.deleted in one
// transaction so a broker outage cannot orphan profile PII without an event.
func TestDeleteAndEnqueue_RemovesUserAndEnqueuesEvent(t *testing.T) {
	requirePostgres(t)
	ctx := t.Context()
	u := newTestUser(t)
	if err := repo.Save(ctx, u); err != nil {
		t.Fatalf("Save: %v", err)
	}

	payload, err := json.Marshal(events.UserDeletedEvent{
		EventType: events.UserDeleted,
		UserID:    u.ID,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := repo.DeleteAndEnqueue(ctx, u.ID, events.UserDeleted, payload); err != nil {
		t.Fatalf("DeleteAndEnqueue: %v", err)
	}

	got, err := repo.FindByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if got != nil {
		t.Fatalf("user row must be gone, got %+v", got)
	}

	pending, err := repo.ListPendingOutbox(ctx, 10)
	if err != nil {
		t.Fatalf("ListPendingOutbox: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending outbox = %d, want 1", len(pending))
	}
	if pending[0].Subject != events.UserDeleted {
		t.Fatalf("subject = %q, want %q", pending[0].Subject, events.UserDeleted)
	}
	if pending[0].AggregateID != u.ID {
		t.Fatalf("aggregate_id = %q, want %q", pending[0].AggregateID, u.ID)
	}
	var ev events.UserDeletedEvent
	if err := json.Unmarshal(pending[0].Payload, &ev); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if ev.UserID != u.ID || ev.EventType != events.UserDeleted {
		t.Fatalf("payload = %+v", ev)
	}
}

func TestDeleteAndEnqueue_NotFound(t *testing.T) {
	requirePostgres(t)
	err := repo.DeleteAndEnqueue(t.Context(), uuid.New().String(), events.UserDeleted, []byte(`{}`))
	if !errors.Is(err, autherrors.ErrUserNotFound) {
		t.Fatalf("got %v, want ErrUserNotFound", err)
	}
}

func TestPasswordRoundtrip(t *testing.T) {
	requirePostgres(t)
	ctx := t.Context()
	u := newTestUser(t)

	if err := repo.Save(ctx, u); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := repo.FindByEmail(ctx, u.Email)
	if err != nil {
		t.Fatalf("FindByEmail: %v", err)
	}

	if !got.ValidatePassword("hunter2") {
		t.Error("password validation failed after round-trip")
	}
	if got.ValidatePassword("wrongpassword") {
		t.Error("wrong password should not validate")
	}
}
