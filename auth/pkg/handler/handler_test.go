package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/elug3/dupli1/auth/pkg/autherrors"
	"github.com/elug3/dupli1/auth/pkg/bootstrap"
	"github.com/elug3/dupli1/auth/pkg/domain"
	"github.com/elug3/dupli1/shared/pkg/permissions"
	"github.com/elug3/dupli1/auth/pkg/handler"
	jwtgen "github.com/elug3/dupli1/auth/pkg/infra/jwt"
	"github.com/elug3/dupli1/auth/pkg/infra/memory"
	"github.com/elug3/dupli1/auth/pkg/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// ---- in-memory UserRepository fake ----------------------------------------

type fakeUserRepo struct {
	mu      sync.RWMutex
	byID    map[string]*domain.User
	byEmail map[string]*domain.User
}

func newFakeUserRepo() *fakeUserRepo {
	return &fakeUserRepo{
		byID:    make(map[string]*domain.User),
		byEmail: make(map[string]*domain.User),
	}
}

func (r *fakeUserRepo) Save(_ context.Context, u *domain.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.byEmail[u.Email]; ok && existing.ID != u.ID {
		return autherrors.ErrUserAlreadyExists
	}
	if old, ok := r.byID[u.ID]; ok && old.Email != u.Email {
		delete(r.byEmail, old.Email)
	}
	r.byID[u.ID] = u
	r.byEmail[u.Email] = u
	return nil
}

func (r *fakeUserRepo) FindByEmail(_ context.Context, email string) (*domain.User, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byEmail[email], nil
}

func (r *fakeUserRepo) FindByID(_ context.Context, id string) (*domain.User, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byID[id], nil
}

func (r *fakeUserRepo) ListAll(_ context.Context) ([]*domain.User, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	users := make([]*domain.User, 0, len(r.byID))
	for _, u := range r.byID {
		users = append(users, u)
	}
	return users, nil
}

func (r *fakeUserRepo) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if u, ok := r.byID[id]; ok {
		delete(r.byEmail, u.Email)
		delete(r.byID, id)
	}
	return nil
}

// ---- test stack ------------------------------------------------------------

type stack struct {
	repo            *fakeUserRepo
	router          *gin.Engine
	registrarToken  string
	sessionStore    *memory.SessionStore
	accessTokenGen  *jwtgen.TokenGenerator
	refreshTokenGen *jwtgen.TokenGenerator
}

func newStack(t *testing.T) *stack {
	t.Helper()
	gin.SetMode(gin.TestMode)

	repo := newFakeUserRepo()
	accessGen := jwtgen.NewTokenGeneratorWithType("access-secret", 900, "access")
	refreshGen := jwtgen.NewTokenGeneratorWithType("refresh-secret", 3600, "refresh")
	sessions := memory.NewSessionStore()

	svc := service.NewService(
		repo,
		accessGen,
		service.WithRefreshTokenGen(refreshGen, time.Hour),
		service.WithSessionStore(sessions),
	)
	h := handler.NewHandler(svc, zerolog.Nop())
	r := bootstrap.NewRouter(h, false, nil, nil, nil)

	registrar, err := domain.NewUser(
		uuid.New().String(),
		"registrar@internal.dupli1",
		"registrar-secret",
		domain.AccountTypeService,
		permissions.UserCreate,
	)
	if err != nil {
		t.Fatalf("NewUser registrar: %v", err)
	}
	if err := repo.Save(t.Context(), registrar); err != nil {
		t.Fatalf("Save registrar: %v", err)
	}
	registrarToken, err := accessGen.Generate(t.Context(), registrar.ID, registrar.Permissions, registrar.Email)
	if err != nil {
		t.Fatalf("Generate registrar token: %v", err)
	}

	return &stack{
		repo:            repo,
		router:          r,
		registrarToken:  registrarToken,
		sessionStore:    sessions,
		accessTokenGen:  accessGen,
		refreshTokenGen: refreshGen,
	}
}

func (s *stack) do(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return s.doWithAuth(t, method, path, "", body)
}

func (s *stack) doWithAuth(t *testing.T, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	return w
}

// registerLoginRefresh registers a user, logs in, and returns a short-lived access token.
func (s *stack) registerLoginRefresh(t *testing.T, email, password string) string {
	t.Helper()

	w := s.doWithAuth(t, http.MethodPost, "/api/v1/auth/register", s.registrarToken, map[string]string{
		"email":    email,
		"password": password,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("register: want 201, got %d: %s", w.Code, w.Body.String())
	}

	w = s.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"email":    email,
		"password": password,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("login: want 200, got %d: %s", w.Code, w.Body.String())
	}

	var loginResp struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(w.Body).Decode(&loginResp); err != nil {
		t.Fatalf("decode login response: %v", err)
	}

	w = s.do(t, http.MethodPost, "/api/v1/auth/refresh", map[string]string{
		"refresh_token": loginResp.RefreshToken,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("refresh: want 200, got %d: %s", w.Code, w.Body.String())
	}

	var refreshResp struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(w.Body).Decode(&refreshResp); err != nil {
		t.Fatalf("decode refresh response: %v", err)
	}
	if refreshResp.Token == "" {
		t.Fatal("access token is empty")
	}
	return refreshResp.Token
}

// ---- POST /register --------------------------------------------------------

func TestRegister(t *testing.T) {
	tests := []struct {
		name     string
		token    string
		body     any
		wantCode int
	}{
		{
			name:     "valid",
			token:    "", // filled in below
			body:     map[string]string{"email": "alice@example.com", "password": "supersecret"},
			wantCode: http.StatusCreated,
		},
		{
			name:     "missing auth",
			token:    "",
			body:     map[string]string{"email": "open@example.com", "password": "supersecret"},
			wantCode: http.StatusUnauthorized,
		},
		{
			name:     "invalid email",
			token:    "",
			body:     map[string]string{"email": "not-an-email", "password": "supersecret"},
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "password too short",
			token:    "",
			body:     map[string]string{"email": "bob@example.com", "password": "short"},
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "missing email",
			token:    "",
			body:     map[string]string{"password": "supersecret"},
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "missing password",
			token:    "",
			body:     map[string]string{"email": "carol@example.com"},
			wantCode: http.StatusBadRequest,
		},
	}

	s := newStack(t)
	tests[0].token = s.registrarToken
	for i := range tests {
		if tests[i].name != "missing auth" && tests[i].token == "" {
			tests[i].token = s.registrarToken
		}
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := s.doWithAuth(t, http.MethodPost, "/api/v1/auth/register", tc.token, tc.body)
			if w.Code != tc.wantCode {
				t.Errorf("want %d, got %d: %s", tc.wantCode, w.Code, w.Body.String())
			}
		})
	}
}

func TestRegister_OpenRegisterAllowsUnauthenticatedCustomer(t *testing.T) {
	gin.SetMode(gin.TestMode)

	repo := newFakeUserRepo()
	accessGen := jwtgen.NewTokenGeneratorWithType("access-secret", 900, "access")
	refreshGen := jwtgen.NewTokenGeneratorWithType("refresh-secret", 3600, "refresh")
	sessions := memory.NewSessionStore()
	svc := service.NewService(
		repo,
		accessGen,
		service.WithRefreshTokenGen(refreshGen, time.Hour),
		service.WithSessionStore(sessions),
	)
	h := handler.NewHandler(svc, zerolog.Nop()).WithOpenRegister(true)
	r := bootstrap.NewRouter(h, false, nil, nil, nil)

	body, _ := json.Marshal(map[string]string{
		"email":        "selfserve@example.com",
		"password":     "supersecret",
		"account_type": "manager", // ignored — forced to customer
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		UserID string `json:"user_id"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	u, err := repo.FindByEmail(t.Context(), "selfserve@example.com")
	if err != nil || u == nil {
		t.Fatalf("user not saved: %v", err)
	}
	if u.AccountType != domain.AccountTypeCustomer {
		t.Fatalf("account_type = %q, want customer", u.AccountType)
	}
	if len(u.Permissions) != 0 {
		t.Fatalf("permissions = %v, want empty", u.Permissions)
	}
	if resp.UserID != u.ID {
		t.Fatalf("user_id = %q, want %q", resp.UserID, u.ID)
	}
}

func TestRegister_BadJSON(t *testing.T) {
	s := newStack(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", bytes.NewBufferString("{bad"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.registrarToken)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}

func TestRegister_DuplicateEmail(t *testing.T) {
	s := newStack(t)
	body := map[string]string{"email": "dup@example.com", "password": "supersecret"}

	w := s.doWithAuth(t, http.MethodPost, "/api/v1/auth/register", s.registrarToken, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("first register: want 201, got %d", w.Code)
	}

	w = s.doWithAuth(t, http.MethodPost, "/api/v1/auth/register", s.registrarToken, body)
	if w.Code != http.StatusConflict {
		t.Errorf("duplicate register: want 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRegister_ResponseContainsUserID(t *testing.T) {
	s := newStack(t)
	w := s.doWithAuth(t, http.MethodPost, "/api/v1/auth/register", s.registrarToken, map[string]string{
		"email": "id@example.com", "password": "supersecret",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d", w.Code)
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, err := uuid.Parse(resp["user_id"]); err != nil {
		t.Errorf("user_id %q is not a valid UUID: %v", resp["user_id"], err)
	}
}

// ---- POST /login -----------------------------------------------------------

func TestLogin(t *testing.T) {
	s := newStack(t)
	const email, password = "login@example.com", "supersecret"

	w := s.doWithAuth(t, http.MethodPost, "/api/v1/auth/register", s.registrarToken, map[string]string{
		"email": email, "password": password,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("setup register: %d", w.Code)
	}

	t.Run("valid credentials", func(t *testing.T) {
		w := s.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{
			"email": email, "password": password,
		})
		if w.Code != http.StatusOK {
			t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
		}
		var resp struct {
			RefreshToken string `json:"refresh_token"`
		}
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.RefreshToken == "" {
			t.Error("refresh_token is empty")
		}
	})

	t.Run("wrong password", func(t *testing.T) {
		w := s.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{
			"email": email, "password": "wrongpassword",
		})
		if w.Code != http.StatusUnauthorized {
			t.Errorf("want 401, got %d", w.Code)
		}
	})

	t.Run("unknown email", func(t *testing.T) {
		w := s.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{
			"email": "nobody@example.com", "password": password,
		})
		if w.Code != http.StatusUnauthorized {
			t.Errorf("want 401, got %d", w.Code)
		}
	})

	t.Run("missing password field", func(t *testing.T) {
		w := s.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{
			"email": email,
		})
		if w.Code != http.StatusBadRequest {
			t.Errorf("want 400, got %d", w.Code)
		}
	})

	t.Run("bad JSON", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString("{bad"))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("want 400, got %d", w.Code)
		}
	})
}

// ---- GET /me ---------------------------------------------------------------

func TestMe(t *testing.T) {
	s := newStack(t)
	const email, password = "me@example.com", "supersecret"
	accessToken := s.registerLoginRefresh(t, email, password)

	t.Run("valid access token", func(t *testing.T) {
		w := s.doWithAuth(t, http.MethodGet, "/api/v1/auth/me", accessToken, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
		}
		var resp struct {
			ID          string `json:"user_id"`
			Email       string `json:"email"`
			AccountType string `json:"account_type"`
		}
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.Email != email {
			t.Errorf("email: got %q, want %q", resp.Email, email)
		}
		if resp.AccountType != domain.AccountTypeCustomer {
			t.Errorf("account_type: got %q, want %q", resp.AccountType, domain.AccountTypeCustomer)
		}
		if _, err := uuid.Parse(resp.ID); err != nil {
			t.Errorf("user_id %q is not a valid UUID", resp.ID)
		}
	})

	t.Run("refresh token rejected", func(t *testing.T) {
		w := s.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{
			"email": email, "password": password,
		})
		if w.Code != http.StatusOK {
			t.Fatalf("login: want 200, got %d", w.Code)
		}
		var loginResp struct {
			RefreshToken string `json:"refresh_token"`
		}
		if err := json.NewDecoder(w.Body).Decode(&loginResp); err != nil {
			t.Fatalf("decode login response: %v", err)
		}

		w = s.doWithAuth(t, http.MethodGet, "/api/v1/auth/me", loginResp.RefreshToken, nil)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("want 401 for refresh token on /me, got %d", w.Code)
		}
	})

	t.Run("missing authorization header", func(t *testing.T) {
		w := s.do(t, http.MethodGet, "/api/v1/auth/me", nil)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("want 401, got %d", w.Code)
		}
	})

	t.Run("invalid token", func(t *testing.T) {
		w := s.doWithAuth(t, http.MethodGet, "/api/v1/auth/me", "this.is.garbage", nil)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("want 401, got %d", w.Code)
		}
	})

	t.Run("malformed bearer header", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
		req.Header.Set("Authorization", accessToken)
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("want 401, got %d", w.Code)
		}
	})
}

func TestMe_RejectsLockedAndDeactivatedAccounts(t *testing.T) {
	const email, password = "me-guard@example.com", "supersecret"

	t.Run("locked account returns 403", func(t *testing.T) {
		s := newStack(t)
		accessToken := s.registerLoginRefresh(t, email, password)

		user, err := s.repo.FindByEmail(t.Context(), email)
		if err != nil || user == nil {
			t.Fatalf("FindByEmail: %v", err)
		}
		user.Lock()
		if err := s.repo.Save(t.Context(), user); err != nil {
			t.Fatalf("Save locked user: %v", err)
		}

		w := s.doWithAuth(t, http.MethodGet, "/api/v1/auth/me", accessToken, nil)
		if w.Code != http.StatusForbidden {
			t.Fatalf("locked /me: want 403, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("deactivated account returns 403", func(t *testing.T) {
		s := newStack(t)
		accessToken := s.registerLoginRefresh(t, "deactivated-me@example.com", password)

		user, err := s.repo.FindByEmail(t.Context(), "deactivated-me@example.com")
		if err != nil || user == nil {
			t.Fatalf("FindByEmail: %v", err)
		}
		user.SetActive(false)
		if err := s.repo.Save(t.Context(), user); err != nil {
			t.Fatalf("Save deactivated user: %v", err)
		}

		w := s.doWithAuth(t, http.MethodGet, "/api/v1/auth/me", accessToken, nil)
		if w.Code != http.StatusForbidden {
			t.Fatalf("deactivated /me: want 403, got %d: %s", w.Code, w.Body.String())
		}
	})
}

// ---- POST /logout ----------------------------------------------------------

func TestLogout(t *testing.T) {
	s := newStack(t)
	const email, password = "logout@example.com", "supersecret"

	w := s.doWithAuth(t, http.MethodPost, "/api/v1/auth/register", s.registrarToken, map[string]string{
		"email": email, "password": password,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("register: want 201, got %d", w.Code)
	}

	w = s.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"email": email, "password": password,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("login: want 200, got %d", w.Code)
	}
	var loginResp struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(w.Body).Decode(&loginResp); err != nil {
		t.Fatalf("decode login response: %v", err)
	}

	t.Run("revokes refresh token", func(t *testing.T) {
		w := s.do(t, http.MethodPost, "/api/v1/auth/logout", map[string]string{
			"refresh_token": loginResp.RefreshToken,
		})
		if w.Code != http.StatusNoContent {
			t.Errorf("want 204, got %d: %s", w.Code, w.Body.String())
		}

		w = s.do(t, http.MethodPost, "/api/v1/auth/refresh", map[string]string{
			"refresh_token": loginResp.RefreshToken,
		})
		if w.Code != http.StatusUnauthorized {
			t.Errorf("want 401 after logout, got %d", w.Code)
		}
	})
}

// ---- POST /refresh ---------------------------------------------------------

func TestRefresh(t *testing.T) {
	s := newStack(t)

	t.Run("valid refresh token returns new token", func(t *testing.T) {
		w := s.doWithAuth(t, http.MethodPost, "/api/v1/auth/register", s.registrarToken, map[string]string{
			"email": "refresh@example.com", "password": "supersecret",
		})
		if w.Code != http.StatusCreated {
			t.Fatalf("register: want 201, got %d", w.Code)
		}

		w = s.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{
			"email": "refresh@example.com", "password": "supersecret",
		})
		if w.Code != http.StatusOK {
			t.Fatalf("login: want 200, got %d", w.Code)
		}
		var loginResp struct {
			RefreshToken string `json:"refresh_token"`
		}
		if err := json.NewDecoder(w.Body).Decode(&loginResp); err != nil {
			t.Fatalf("decode login response: %v", err)
		}

		w = s.do(t, http.MethodPost, "/api/v1/auth/refresh", map[string]string{
			"refresh_token": loginResp.RefreshToken,
		})
		if w.Code != http.StatusOK {
			t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
		}
		var resp struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.Token == "" {
			t.Error("token is empty")
		}
	})

	t.Run("rotates the refresh token and invalidates the old one", func(t *testing.T) {
		w := s.doWithAuth(t, http.MethodPost, "/api/v1/auth/register", s.registrarToken, map[string]string{
			"email": "rotate@example.com", "password": "supersecret",
		})
		if w.Code != http.StatusCreated {
			t.Fatalf("register: want 201, got %d", w.Code)
		}

		w = s.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{
			"email": "rotate@example.com", "password": "supersecret",
		})
		var loginResp struct {
			RefreshToken string `json:"refresh_token"`
		}
		if err := json.NewDecoder(w.Body).Decode(&loginResp); err != nil {
			t.Fatalf("decode login response: %v", err)
		}

		w = s.do(t, http.MethodPost, "/api/v1/auth/refresh", map[string]string{
			"refresh_token": loginResp.RefreshToken,
		})
		if w.Code != http.StatusOK {
			t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
		}
		var refreshResp struct {
			Token        string `json:"token"`
			RefreshToken string `json:"refresh_token"`
		}
		if err := json.NewDecoder(w.Body).Decode(&refreshResp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if refreshResp.RefreshToken == "" || refreshResp.RefreshToken == loginResp.RefreshToken {
			t.Fatalf("expected a new, different refresh token; got %q (original %q)", refreshResp.RefreshToken, loginResp.RefreshToken)
		}

		w = s.do(t, http.MethodPost, "/api/v1/auth/refresh", map[string]string{
			"refresh_token": loginResp.RefreshToken,
		})
		if w.Code != http.StatusUnauthorized {
			t.Errorf("reusing the rotated-away token: want 401, got %d", w.Code)
		}

		w = s.do(t, http.MethodPost, "/api/v1/auth/refresh", map[string]string{
			"refresh_token": refreshResp.RefreshToken,
		})
		if w.Code != http.StatusOK {
			t.Errorf("refreshing with the rotated token: want 200, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("invalid token", func(t *testing.T) {
		w := s.do(t, http.MethodPost, "/api/v1/auth/refresh", map[string]string{
			"refresh_token": "bad.token.value",
		})
		if w.Code != http.StatusUnauthorized {
			t.Errorf("want 401, got %d", w.Code)
		}
	})

	t.Run("missing refresh_token field", func(t *testing.T) {
		w := s.do(t, http.MethodPost, "/api/v1/auth/refresh", map[string]string{})
		if w.Code != http.StatusBadRequest {
			t.Errorf("want 400, got %d", w.Code)
		}
	})
}

// ---- DELETE /users/:id -----------------------------------------------------

func (s *stack) userDeleteToken(t *testing.T) string {
	t.Helper()
	admin, err := domain.NewUser(
		uuid.New().String(),
		"user-admin@internal.dupli1",
		"admin-secret",
		domain.AccountTypeManager,
		permissions.UserDelete,
		permissions.UserPasswordUpdate,
		permissions.UserStatusUpdate,
	)
	if err != nil {
		t.Fatalf("NewUser admin: %v", err)
	}
	if err := s.repo.Save(t.Context(), admin); err != nil {
		t.Fatalf("Save admin: %v", err)
	}
	token, err := s.accessTokenGen.Generate(t.Context(), admin.ID, admin.Permissions, admin.Email)
	if err != nil {
		t.Fatalf("Generate admin token: %v", err)
	}
	return token
}

func TestDeleteUser(t *testing.T) {
	t.Run("requires user.delete permission", func(t *testing.T) {
		s := newStack(t)
		customer, err := domain.NewUser(uuid.New().String(), "cust@example.com", "password12", domain.AccountTypeCustomer)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.repo.Save(t.Context(), customer); err != nil {
			t.Fatal(err)
		}

		w := s.doWithAuth(t, http.MethodDelete, "/api/v1/auth/users/"+customer.ID, s.registrarToken, nil)
		if w.Code != http.StatusForbidden {
			t.Fatalf("registrar without user.delete: want 403, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("deletes customer and returns 204", func(t *testing.T) {
		s := newStack(t)
		adminToken := s.userDeleteToken(t)
		customer, err := domain.NewUser(uuid.New().String(), "delete-me@example.com", "password12", domain.AccountTypeCustomer)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.repo.Save(t.Context(), customer); err != nil {
			t.Fatal(err)
		}

		w := s.doWithAuth(t, http.MethodDelete, "/api/v1/auth/users/"+customer.ID, adminToken, nil)
		if w.Code != http.StatusNoContent {
			t.Fatalf("want 204, got %d: %s", w.Code, w.Body.String())
		}
		if got, _ := s.repo.FindByID(t.Context(), customer.ID); got != nil {
			t.Fatal("user row should be removed")
		}
	})

	t.Run("returns 404 for missing user", func(t *testing.T) {
		s := newStack(t)
		adminToken := s.userDeleteToken(t)

		w := s.doWithAuth(t, http.MethodDelete, "/api/v1/auth/users/"+uuid.New().String(), adminToken, nil)
		if w.Code != http.StatusNotFound {
			t.Fatalf("want 404, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("forbids self-delete", func(t *testing.T) {
		s := newStack(t)
		admin, err := domain.NewUser(
			uuid.New().String(),
			"self-delete@internal.dupli1",
			"admin-secret",
			domain.AccountTypeManager,
			permissions.UserDelete,
			permissions.UserPasswordUpdate,
			permissions.UserStatusUpdate,
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.repo.Save(t.Context(), admin); err != nil {
			t.Fatal(err)
		}
		token, err := s.accessTokenGen.Generate(t.Context(), admin.ID, admin.Permissions, admin.Email)
		if err != nil {
			t.Fatal(err)
		}

		w := s.doWithAuth(t, http.MethodDelete, "/api/v1/auth/users/"+admin.ID, token, nil)
		if w.Code != http.StatusForbidden {
			t.Fatalf("self-delete: want 403, got %d: %s", w.Code, w.Body.String())
		}
	})
}
