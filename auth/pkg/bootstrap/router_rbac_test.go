package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/elug3/dupli1/auth/pkg/domain"
	"github.com/elug3/dupli1/auth/pkg/handler"
	jwtgen "github.com/elug3/dupli1/auth/pkg/infra/jwt"
	"github.com/elug3/dupli1/auth/pkg/service"
	"github.com/elug3/dupli1/shared/pkg/permissions"
	"github.com/elug3/dupli1/shared/pkg/settings"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

type rbacFakeRepo struct {
	byID    map[string]*domain.User
	byEmail map[string]*domain.User
}

func newRBACFakeRepo() *rbacFakeRepo {
	return &rbacFakeRepo{
		byID:    make(map[string]*domain.User),
		byEmail: make(map[string]*domain.User),
	}
}

func (r *rbacFakeRepo) Save(_ context.Context, u *domain.User) error {
	r.byID[u.ID] = u
	r.byEmail[u.Email] = u
	return nil
}

func (r *rbacFakeRepo) FindByEmail(_ context.Context, email string) (*domain.User, error) {
	return r.byEmail[email], nil
}

func (r *rbacFakeRepo) FindByID(_ context.Context, id string) (*domain.User, error) {
	return r.byID[id], nil
}

func (r *rbacFakeRepo) ListAll(_ context.Context) ([]*domain.User, error) {
	return nil, nil
}

func (r *rbacFakeRepo) Delete(_ context.Context, id string) error {
	delete(r.byID, id)
	return nil
}

func TestOwnerCanRegisterAndListUsers(t *testing.T) {
	gin.SetMode(gin.TestMode)

	repo := newRBACFakeRepo()
	accessGen := jwtgen.NewTokenGeneratorWithType("test-secret", 900, "access")
	refreshGen := jwtgen.NewTokenGeneratorWithType("test-secret", 3600, "refresh")
	svc := service.NewService(repo, accessGen, service.WithRefreshTokenGen(refreshGen, time.Hour))
	h := handler.NewHandler(svc, zerolog.Nop())

	owner, err := domain.NewUser(
		uuid.New().String(),
		"admin@dupli1.com",
		"password",
		domain.AccountTypeManager,
		permissions.All,
	)
	if err != nil {
		t.Fatalf("NewUser: %v", err)
	}
	if err := repo.Save(t.Context(), owner); err != nil {
		t.Fatalf("Save owner: %v", err)
	}

	accessToken, err := accessGen.Generate(t.Context(), owner.ID, owner.Permissions, owner.Email)
	if err != nil {
		t.Fatalf("Generate token: %v", err)
	}

	r := newRouter(h, false, nil, nil, nil, settings.NewResponse("auth"))

	body, _ := json.Marshal(map[string]string{
		"email":    "new-user@example.com",
		"password": "supersecret",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("register: want 201, got %d: %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/auth/users", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list users: want 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCustomerRegistrarCanRegisterButNotManageUsers(t *testing.T) {
	gin.SetMode(gin.TestMode)

	repo := newRBACFakeRepo()
	accessGen := jwtgen.NewTokenGeneratorWithType("test-secret", 900, "access")
	refreshGen := jwtgen.NewTokenGeneratorWithType("test-secret", 3600, "refresh")
	svc := service.NewService(repo, accessGen, service.WithRefreshTokenGen(refreshGen, time.Hour))
	h := handler.NewHandler(svc, zerolog.Nop())

	registrar, err := domain.NewUser(
		uuid.New().String(),
		"dupli1-web@internal.dupli1",
		"service-secret",
		domain.AccountTypeService,
		permissions.UserCreate,
	)
	if err != nil {
		t.Fatalf("NewUser: %v", err)
	}
	if err := repo.Save(t.Context(), registrar); err != nil {
		t.Fatalf("Save registrar: %v", err)
	}

	accessToken, err := accessGen.Generate(t.Context(), registrar.ID, registrar.Permissions, registrar.Email)
	if err != nil {
		t.Fatalf("Generate token: %v", err)
	}

	r := newRouter(h, false, nil, nil, nil, settings.NewResponse("auth"))

	body, _ := json.Marshal(map[string]string{
		"email":    "new-customer@example.com",
		"password": "supersecret",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("register: want 201, got %d: %s", w.Code, w.Body.String())
	}

	patchBody, _ := json.Marshal(map[string]string{"password": "anothersecret"})
	req = httptest.NewRequest(http.MethodPatch, "/api/v1/auth/users/some-id/password", bytes.NewReader(patchBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("password patch: want 403, got %d: %s", w.Code, w.Body.String())
	}
}
