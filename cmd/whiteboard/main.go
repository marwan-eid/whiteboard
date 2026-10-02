// Command whiteboard runs one board node.
//
// Usage:
//
//	whiteboard              run the node (configured via env, see internal/config)
//	whiteboard healthcheck  exit 0 if the local node answers /healthz (for containers)
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/time/rate"

	"whiteboard/internal/access"
	"whiteboard/internal/board"
	"whiteboard/internal/config"
	"whiteboard/internal/db"
	"whiteboard/internal/gateway"
	"whiteboard/internal/httpapi"
	"whiteboard/internal/metrics"
	"whiteboard/internal/ratelimit"
	"whiteboard/internal/store"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel})).
		With("node", cfg.NodeID)
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL, 30*time.Second)
	if err != nil {
		return err
	}
	defer pool.Close()

	applied, err := db.Migrate(ctx, pool)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if len(applied) > 0 {
		log.Info("applied migrations", "migrations", applied)
	}

	st, err := store.NewPostgres(pool)
	if err != nil {
		return err
	}
	secret := []byte(cfg.Secret)
	if len(secret) == 0 {
		secret = make([]byte, 32)
		_, _ = rand.Read(secret)
		log.Warn("SECRET is not set; using a random one, so guest tokens will not survive a restart")
	}
	signer := access.NewSigner(secret)
	accessStore := access.NewPostgres(pool)

	m := metrics.New()
	boards := board.NewRegistry(board.Config{NodeID: cfg.NodeID, Store: st}, log, m)
	gw := gateway.New(gateway.Config{
		Authorizer: accessStore,
		Signer:     signer,
		ConnsPerIP: ratelimit.NewCounter(cfg.MaxConnsPerIP),
		TrustProxy: cfg.TrustProxy,
	}, boards, log, m)
	srv := &http.Server{
		Addr: cfg.Addr,
		Handler: httpapi.NewRouter(httpapi.Deps{
			Gateway:  gw,
			Metrics:  m,
			Ready:    pool.Ping,
			Signer:   signer,
			Boards:   accessStore,
			KickLink: boards.KickLink,
			// 30 new private boards an hour per IP, 10 at once.
			BoardCreates: ratelimit.NewKeyed(rate.Every(2*time.Minute), 10),
			TrustProxy:   cfg.TrustProxy,
			NodeID:       cfg.NodeID,
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Addr)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	log.Info("shutting down")
	gw.Shutdown()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Commit what boards have accepted and snapshot them, so the next start loads fast.
	closeErr := boards.Close(shutdownCtx)
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return errors.Join(err, closeErr)
	}
	return closeErr
}

func healthcheck() int {
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8081"
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bad ADDR:", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+port+"/healthz", http.NoBody)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "status", resp.StatusCode)
		return 1
	}
	return 0
}
