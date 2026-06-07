package bootstrap

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"

	_ "github.com/lib/pq"
)

func openPostgres(ctx context.Context, connURL string, maxConns int) (*sql.DB, error) {
	if connURL == "" {
		return nil, fmt.Errorf("database URL is required")
	}

	connURL = withSSLDisabled(connURL)

	db, err := sql.Open("postgres", connURL)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}

	if maxConns > 0 {
		db.SetMaxOpenConns(maxConns)
	}

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	return db, nil
}

// withSSLDisabled ensures postgres connections do not require SSL unless
// sslmode is already set in the connection string.
func withSSLDisabled(connURL string) string {
	if strings.Contains(connURL, "sslmode=") {
		return connURL
	}

	if strings.HasPrefix(connURL, "postgres://") || strings.HasPrefix(connURL, "postgresql://") {
		parsed, err := url.Parse(connURL)
		if err != nil {
			sep := "?"
			if strings.Contains(connURL, "?") {
				sep = "&"
			}
			return connURL + sep + "sslmode=disable"
		}

		query := parsed.Query()
		query.Set("sslmode", "disable")
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}

	if !strings.Contains(connURL, " ") {
		return connURL + " sslmode=disable"
	}
	return strings.TrimSpace(connURL) + " sslmode=disable"
}
