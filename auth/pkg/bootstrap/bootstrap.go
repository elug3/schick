package bootstrap

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/elug3/dupli1/auth/pkg/handler"
	jwtinfra "github.com/elug3/dupli1/auth/pkg/infra/jwt"
	memoryinfra "github.com/elug3/dupli1/auth/pkg/infra/memory"
	natsinfra "github.com/elug3/dupli1/auth/pkg/infra/nats"
	"github.com/elug3/dupli1/auth/pkg/infra/postgres"
	redisinfra "github.com/elug3/dupli1/auth/pkg/infra/redis"
	"github.com/elug3/dupli1/auth/pkg/ports"
	"github.com/elug3/dupli1/auth/pkg/service"
	"github.com/elug3/dupli1/shared/pkg/outbox"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// sessionGCInterval is how often the in-memory session store fallback sweeps
// expired refresh-token sessions when no Redis is configured.
const sessionGCInterval = 10 * time.Minute

// App holds wired auth service dependencies and the HTTP router.
type App struct {
	Engine  *gin.Engine
	Handler *handler.Handler
	DB      *sql.DB
	Redis   *redis.Client
	close   func() error
}

// Close releases infrastructure resources opened during bootstrap.
func (a *App) Close() error {
	if a == nil || a.close == nil {
		return nil
	}
	return a.close()
}

// Bootstrap wires infrastructure, services, handlers, and HTTP routes.
func Bootstrap(ctx context.Context, cfg Config) (*App, error) {
	if cfg.TokenExpiry <= 0 {
		return nil, fmt.Errorf("token expiry must be > 0")
	}
	if cfg.RefreshTokenExpiry <= 0 {
		return nil, fmt.Errorf("refresh token expiry must be > 0")
	}

	accessTokenGen, refreshTokenGen, jwksJSON, err := buildTokenGenerators(cfg)
	if err != nil {
		return nil, err
	}

	db, err := openPostgres(ctx, cfg.DBURL, cfg.MaxConns, cfg.Logger)
	if err != nil {
		return nil, err
	}

	redisClient, err := openRedis(ctx, cfg.RedisURL)
	if err != nil {
		_ = db.Close()
		return nil, err
	}

	var eventPublisher ports.EventPublisher
	var natsPublisher *natsinfra.Publisher
	if cfg.NATSURL != "" {
		natsPublisher, err = natsinfra.NewPublisher(cfg.NATSURL)
		if err != nil {
			if redisClient != nil {
				_ = redisClient.Close()
			}
			_ = db.Close()
			return nil, err
		}
		eventPublisher = natsPublisher
	}

	if err := migrateSchema(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	userRepo := postgres.NewUserRepository(db)
	outboxDrainer := outbox.NewDrainer(userRepo, eventPublisher, "auth outbox drain")

	var sessionStore ports.SessionStore
	if redisClient != nil {
		sessionStore = redisinfra.NewSessionCache(redisClient)
	} else {
		// Without this, a nil sessionStore makes Logout a silent no-op and
		// Refresh skips revocation checks entirely — "logged out" refresh
		// tokens would keep minting access tokens until they naturally
		// expire. Fall back to an in-memory store so revocation still works
		// on a single instance; only cross-replica/restart durability is lost.
		cfg.Logger.Warn().
			Str("event", "auth_session_store_in_memory_fallback").
			Msg("no Redis configured — refresh-token session revocation is tracked in-memory only and will not survive a restart or work across multiple auth replicas")
		mem := memoryinfra.NewSessionStore()
		mem.GC(ctx, sessionGCInterval)
		sessionStore = mem
	}

	svc := service.NewService(
		userRepo,
		accessTokenGen,
		service.WithRefreshTokenGen(refreshTokenGen, cfg.RefreshTokenExpiry),
		service.WithSessionStore(sessionStore),
		service.WithEventPublisher(eventPublisher),
		service.WithOutboxDrainer(outboxDrainer),
		service.WithLogger(cfg.Logger),
	)

	// Long-lived worker root; cancelled on process shutdown.
	workerCtx, workerCancel := context.WithCancel(context.Background())
	svc.StartOutboxWorker(workerCtx, 2*time.Second)

	h := handler.NewHandler(svc, cfg.Logger).WithOpenRegister(cfg.OpenRegister)
	if cfg.OpenRegister {
		cfg.Logger.Warn().Str("event", "open_register_enabled").Msg("TEMPORARY: unauthenticated customer register is enabled")
	}
	engine := newRouter(h, cfg.Debug, jwksJSON, redisClient, cfg.CORSOrigins, BuildSettings(cfg))

	app := &App{
		Engine:  engine,
		Handler: h,
		DB:      db,
		Redis:   redisClient,
		close: func() error {
			workerCancel()
			var errs []error
			if redisClient != nil {
				errs = append(errs, redisClient.Close())
			}
			if natsPublisher != nil {
				natsPublisher.Close()
			}
			errs = append(errs, db.Close())
			return errors.Join(errs...)
		},
	}

	if err := seedOwner(ctx, cfg, userRepo); err != nil {
		_ = app.Close()
		return nil, err
	}
	if err := seedWebServiceAccount(ctx, cfg, userRepo); err != nil {
		_ = app.Close()
		return nil, err
	}
	if err := seedOrderServiceAccount(ctx, cfg, userRepo); err != nil {
		_ = app.Close()
		return nil, err
	}

	return app, nil
}

// buildTokenGenerators creates RS256 access and refresh token generators.
// When JWTPrivateKeyPEM is empty, an ephemeral 2048-bit RSA key is generated
// (dev mode only — tokens are invalidated on restart).
func buildTokenGenerators(cfg Config) (access ports.TokenGenerator, refresh ports.TokenGenerator, jwksJSON []byte, err error) {
	if len(cfg.JWTPrivateKeyPEM) > 0 {
		access, jwksJSON, err = newRSAGeneratorWithJWKS(cfg.JWTPrivateKeyPEM, cfg.JWTKeyID, int64(cfg.TokenExpiry.Seconds()), "access")
		if err != nil {
			return nil, nil, nil, err
		}
		refresh, _, err = newRSAGeneratorWithJWKS(cfg.JWTPrivateKeyPEM, cfg.JWTKeyID, int64(cfg.RefreshTokenExpiry.Seconds()), "refresh")
		if err != nil {
			return nil, nil, nil, err
		}
		return access, refresh, jwksJSON, nil
	}

	// No key configured — generate a throwaway RSA key. Tokens are invalid across restarts.
	cfg.Logger.Warn().Msg("no JWT_PRIVATE_KEY or JWT_PRIVATE_KEY_FILE configured — generating ephemeral RSA-2048 key; every issued token is invalidated on restart")
	key, genErr := jwtinfra.GenerateRSAKey(2048)
	if genErr != nil {
		return nil, nil, nil, fmt.Errorf("generate ephemeral RSA key: %w", genErr)
	}
	rsaAccess := jwtinfra.NewRSATokenGeneratorWithType(key, cfg.JWTKeyID, int64(cfg.TokenExpiry.Seconds()), "access")
	rsaRefresh := jwtinfra.NewRSATokenGeneratorWithType(key, cfg.JWTKeyID, int64(cfg.RefreshTokenExpiry.Seconds()), "refresh")
	jwksJSON, err = json.Marshal(rsaAccess.PublicJWKS())
	if err != nil {
		return nil, nil, nil, fmt.Errorf("marshal JWKS: %w", err)
	}
	return rsaAccess, rsaRefresh, jwksJSON, nil
}

func newRSAGeneratorWithJWKS(pemBytes []byte, keyID string, expirySeconds int64, tokenType string) (*jwtinfra.RSATokenGenerator, []byte, error) {
	gen, err := jwtinfra.NewRSATokenGeneratorFromPEM(pemBytes, keyID, expirySeconds, tokenType)
	if err != nil {
		return nil, nil, err
	}
	jwksJSON, err := json.Marshal(gen.PublicJWKS())
	if err != nil {
		return nil, nil, fmt.Errorf("marshal JWKS: %w", err)
	}
	return gen, jwksJSON, nil
}
