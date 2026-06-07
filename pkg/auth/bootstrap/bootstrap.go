package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/elug3/schick/pkg/auth/handler"
	"github.com/elug3/schick/pkg/auth/infra/jwt"
	"github.com/elug3/schick/pkg/auth/infra/postgres"
	"github.com/elug3/schick/pkg/auth/service"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// App holds wired auth service dependencies and the HTTP router.
type App struct {
	Engine *gin.Engine
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
	if len(cfg.TokenSigningKey) == 0 {
		return nil, fmt.Errorf("token signing key is required")
	}
	if cfg.TokenExpiry <= 0 {
		return nil, fmt.Errorf("token expiry must be > 0")
	}

	db, err := openPostgres(ctx, cfg.DBURL, cfg.MaxConns)
	if err != nil {
		return nil, err
	}

	redisClient, err := openRedis(ctx, cfg.RedisURL)
	if err != nil {
		_ = db.Close()
		return nil, err
	}

	userRepo := postgres.NewUserRepository(db)
	tokenGen := jwt.NewTokenGenerator(
		string(cfg.TokenSigningKey),
		int64(cfg.TokenExpiry.Seconds()),
	)
	svc := service.NewService(userRepo, tokenGen)
	h := handler.NewHandler(svc)
	engine := newRouter(h, cfg.Debug)

	app := &App{
		Engine:  engine,
		Handler: h,
		DB:      db,
		Redis:   redisClient,
		close: func() error {
			var errs []error
			if redisClient != nil {
				errs = append(errs, redisClient.Close())
			}
			errs = append(errs, db.Close())
			return errors.Join(errs...)
		},
	}

	return app, nil
}
