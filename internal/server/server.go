// Package server provides HTTP server setup and graceful shutdown for the router.
package server

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Config holds addresses and timing for the GraphQL and health check servers.
type Config struct {
	GraphQLAddr     string
	GraphQLPath     string
	HealthAddr      string
	HealthPath      string
	ShutdownTimeout time.Duration
}

// Serve starts the GraphQL and health servers and blocks until ctx is cancelled,
// then shuts both down gracefully within ShutdownTimeout.
func Serve(ctx context.Context, cfg Config, graphqlHandler, healthHandler http.Handler) error {
	graphqlMux := http.NewServeMux()
	graphqlMux.Handle(cfg.GraphQLPath, graphqlHandler)

	healthMux := http.NewServeMux()
	healthMux.Handle(cfg.HealthPath, healthHandler)

	graphqlSrv := &http.Server{Addr: cfg.GraphQLAddr, Handler: graphqlMux}
	healthSrv := &http.Server{Addr: cfg.HealthAddr, Handler: healthMux}

	errs := make(chan error, 2)
	var wg sync.WaitGroup

	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := graphqlSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errs <- fmt.Errorf("server: graphql: %w", err)
		}
	}()
	go func() {
		defer wg.Done()
		if err := healthSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errs <- fmt.Errorf("server: health: %w", err)
		}
	}()

	<-ctx.Done()

	shutCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	_ = graphqlSrv.Shutdown(shutCtx)
	_ = healthSrv.Shutdown(shutCtx)
	wg.Wait()
	close(errs)

	for err := range errs {
		return err
	}
	return nil
}
