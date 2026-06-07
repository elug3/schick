package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/elug3/schick/pkg/auth"
)

var usageStr = `
Usage: schick-auth [OPTIONS]

An auth server application that serves authentication APIs over HTTP.

Options:
  -host string
      Server host address (default from SCHICK_AUTH_ADDR or :8080)
  -port int
      Server port number
  -addr string
      Server listen address (overrides host/port)
  -db string
      Database connection URL
  -redis string
      Redis connection URL
  -jwt-secret string
      JWT signing secret (required; also JWT_SECRET env)
  -help
      Show this help message

Environment variables:
  SERVER_HOST, SERVER_PORT, SCHICK_AUTH_ADDR, JWT_SECRET, DB_URL, REDIS_URL

Examples:
  schick-auth -jwt-secret dev-secret
  schick-auth -port 9000 -host 0.0.0.0 -jwt-secret dev-secret
  JWT_SECRET=dev-secret schick-auth
`

func main() {
	fs := flag.NewFlagSet("schick-auth", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, usageStr)
	}

	opts, err := ConfigureOptions(fs, os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "ConfigureOptions: %v\n", err)
		os.Exit(1)
	}

	srv, err := auth.NewServer(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "NewServer: %v\n", err)
		os.Exit(1)
	}

	interrupt, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	runErr := make(chan error, 1)
	go func() {
		runErr <- srv.Run()
	}()

	select {
	case err := <-runErr:
		if err != nil {
			fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		}
	case <-interrupt.Done():
	}

	srv.StopAndWait()
}
