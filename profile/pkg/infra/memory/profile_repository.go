package memory

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/elug3/dupli1/profile/pkg/domain"
	"github.com/elug3/dupli1/profile/pkg/ports"
)

// ProfileRepository is an in-memory profile store for tests and the
// no-database fallback.
type ProfileRepository struct {
	mu        sync.Mutex
	profiles  map[string]*domain.Profile
	addresses map[string]*domain.Address
	addrSeq   int64
}

// NewProfileRepository creates an empty in-memory profile repository.
func NewProfileRepository() *ProfileRepository {
	return &ProfileRepository{
		profiles:  make(map[string]*domain.Profile),
		addresses: make(map[string]*domain.Address),
	}
}

func (r *ProfileRepository) GetProfile(_ context.Context, userID string) (*domain.Profile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.profiles[userID]
	if !ok {
		return nil, nil
	}
	cp := *p
	return &cp, nil
}

func (r *ProfileRepository) UpsertProfile(_ context.Context, profile *domain.Profile) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *profile
	r.profiles[profile.UserID] = &cp
	return nil
}

func (r *ProfileRepository) ListAddresses(_ context.Context, userID string) ([]*domain.Address, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*domain.Address
	for _, a := range r.addresses {
		if a.UserID == userID {
			cp := *a
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDefault != out[j].IsDefault {
			return out[i].IsDefault
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func (r *ProfileRepository) CountAddresses(_ context.Context, userID string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, a := range r.addresses {
		if a.UserID == userID {
			n++
		}
	}
	return n, nil
}

func (r *ProfileRepository) GetAddress(_ context.Context, userID, addressID string) (*domain.Address, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.addresses[addressID]
	if !ok || a.UserID != userID {
		return nil, nil
	}
	cp := *a
	return &cp, nil
}

func (r *ProfileRepository) SaveAddress(_ context.Context, address *domain.Address) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.addresses[address.ID]; ok && existing.UserID != address.UserID {
		return ports.ErrAddressNotFound
	}
	cp := *address
	r.addresses[address.ID] = &cp
	return nil
}

func (r *ProfileRepository) DeleteAddress(_ context.Context, userID, addressID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.addresses[addressID]
	if !ok || a.UserID != userID {
		return ports.ErrAddressNotFound
	}
	delete(r.addresses, addressID)
	return nil
}

func (r *ProfileRepository) ClearDefaultAddresses(_ context.Context, userID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UTC()
	for _, a := range r.addresses {
		if a.UserID == userID && a.IsDefault {
			a.IsDefault = false
			a.UpdatedAt = now
		}
	}
	return nil
}

func (r *ProfileRepository) NextAddressID(_ context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.addrSeq++
	return fmt.Sprintf("addr_%06d", r.addrSeq), nil
}

// DeleteUserData removes userID's profile and all saved addresses.
func (r *ProfileRepository) DeleteUserData(_ context.Context, userID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, a := range r.addresses {
		if a.UserID == userID {
			delete(r.addresses, id)
		}
	}
	delete(r.profiles, userID)
	return nil
}
