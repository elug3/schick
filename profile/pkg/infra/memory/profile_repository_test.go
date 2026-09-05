package memory

import (
	"testing"
	"time"

	"github.com/elug3/dupli1/profile/pkg/domain"
	"github.com/elug3/dupli1/profile/pkg/ports"
)

func TestSaveAddressRefusesCrossUserOverwrite(t *testing.T) {
	repo := NewProfileRepository()
	now := time.Now().UTC()
	original := &domain.Address{
		ID: "addr_000001", UserID: "user-a", Label: "home",
		RecipientName: "A", RecipientPhone: "01011112222",
		PostalCode: "06000", AddressLine1: "line", City: "서울", Province: "서울",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := repo.SaveAddress(t.Context(), original); err != nil {
		t.Fatalf("SaveAddress original: %v", err)
	}

	hijack := *original
	hijack.UserID = "user-b"
	hijack.Label = "stolen"
	if err := repo.SaveAddress(t.Context(), &hijack); err != ports.ErrAddressNotFound {
		t.Fatalf("cross-user overwrite err = %v, want ErrAddressNotFound", err)
	}

	got, err := repo.GetAddress(t.Context(), "user-a", "addr_000001")
	if err != nil || got == nil {
		t.Fatalf("GetAddress: %v %v", got, err)
	}
	if got.Label != "home" || got.UserID != "user-a" {
		t.Fatalf("original address mutated: %+v", got)
	}
}
