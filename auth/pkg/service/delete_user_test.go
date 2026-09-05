package service

import (
	"context"
	"errors"
	"testing"

	"github.com/elug3/dupli1/auth/pkg/autherrors"
	"github.com/elug3/dupli1/auth/pkg/domain"
	"github.com/elug3/dupli1/shared/pkg/events"
)

type deleteUserRepo struct {
	user    *domain.User
	deleted string
}

func (r *deleteUserRepo) FindByEmail(context.Context, string) (*domain.User, error) {
	return nil, nil
}
func (r *deleteUserRepo) FindByID(_ context.Context, id string) (*domain.User, error) {
	if r.user == nil || r.user.ID != id {
		return nil, nil
	}
	return r.user, nil
}
func (r *deleteUserRepo) ListAll(context.Context) ([]*domain.User, error) { return nil, nil }
func (r *deleteUserRepo) Save(context.Context, *domain.User) error        { return nil }
func (r *deleteUserRepo) Delete(_ context.Context, id string) error {
	r.deleted = id
	r.user = nil
	return nil
}

type recordingPublisher struct {
	subject string
	event   any
}

func (p *recordingPublisher) Publish(_ context.Context, subject string, event any) error {
	p.subject = subject
	p.event = event
	return nil
}

func TestDeleteUser_PublishesUserDeleted(t *testing.T) {
	user, _ := domain.NewUser("u-del", "del@example.com", "password12", domain.AccountTypeCustomer)
	repo := &deleteUserRepo{user: user}
	pub := &recordingPublisher{}
	svc := NewService(repo, fakeTokenGenerator{}, WithEventPublisher(pub))

	if err := svc.DeleteUser(context.Background(), "u-del"); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if repo.deleted != "u-del" {
		t.Fatalf("deleted = %q, want u-del", repo.deleted)
	}
	if pub.subject != events.UserDeleted {
		t.Fatalf("subject = %q, want %q", pub.subject, events.UserDeleted)
	}
	ev, ok := pub.event.(events.UserDeletedEvent)
	if !ok {
		t.Fatalf("event type %T, want UserDeletedEvent", pub.event)
	}
	if ev.UserID != "u-del" || ev.EventType != events.UserDeleted {
		t.Fatalf("event = %+v", ev)
	}
}

func TestDeleteUser_NotFound(t *testing.T) {
	svc := NewService(&deleteUserRepo{}, fakeTokenGenerator{})
	if err := svc.DeleteUser(context.Background(), "missing"); !errors.Is(err, autherrors.ErrUserNotFound) {
		t.Fatalf("got %v, want ErrUserNotFound", err)
	}
}

type failingPublisher struct{}

func (failingPublisher) Publish(context.Context, string, any) error {
	return errors.New("broker down")
}

func TestDeleteUser_FailsWhenPublishFails(t *testing.T) {
	user, _ := domain.NewUser("u-del-fail", "fail@example.com", "password12", domain.AccountTypeCustomer)
	repo := &deleteUserRepo{user: user}
	svc := NewService(repo, fakeTokenGenerator{}, WithEventPublisher(failingPublisher{}))

	if err := svc.DeleteUser(context.Background(), "u-del-fail"); err == nil {
		t.Fatal("DeleteUser must fail when user.deleted cannot be published")
	}
	if repo.deleted != "" {
		t.Fatalf("deleted = %q, want empty — the row must remain if publish fails", repo.deleted)
	}
	if repo.user == nil {
		t.Fatal("user row must remain when publish fails")
	}
}
