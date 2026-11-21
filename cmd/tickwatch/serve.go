package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/chaggangamer/tickwatch/internal/alert"
	"github.com/chaggangamer/tickwatch/internal/api"
	"github.com/chaggangamer/tickwatch/internal/checker"
	"github.com/chaggangamer/tickwatch/internal/config"
	"github.com/chaggangamer/tickwatch/internal/incident"
	"github.com/chaggangamer/tickwatch/internal/scheduler"
	"github.com/chaggangamer/tickwatch/internal/store"
	"github.com/chaggangamer/tickwatch/internal/web"
)

type combinedHandler struct {
	apiHandler http.Handler
	webHandler http.Handler
}

func (c combinedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if p == "/healthz" || p == "/ping" || strings.HasPrefix(p, "/ping/") || strings.HasPrefix(p, "/api/v1/") {
		c.apiHandler.ServeHTTP(w, r)
		return
	}
	c.webHandler.ServeHTTP(w, r)
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "path to config file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	log := newLogger(cfg.Log.Level)

	st, err := store.Open(cfg.Database.Path, log)
	if err != nil {
		return err
	}
	defer st.Close()

	if err := bootstrapAdmin(st, cfg, log); err != nil {
		return err
	}

	runner := checker.New(checker.Options{BlockPrivate: cfg.SSRF.BlockPrivate})
	tracker := incident.New(st, log)
	monitors, err := st.Monitors()
	if err != nil {
		return fmt.Errorf("load monitors: %w", err)
	}
	open, err := st.OpenIncidents()
	if err != nil {
		return fmt.Errorf("load incidents: %w", err)
	}
	tracker.Load(monitors, open)

	alerts := alert.New(st, log)
	sched := scheduler.New(scheduler.Deps{
		Store:   st,
		Runner:  runner,
		Tracker: tracker,
		Alerts:  alerts,
		Workers: cfg.Scheduler.Workers,
		Log:     log,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	alerts.Start(ctx)
	go func() {
		if err := sched.Run(ctx); err != nil {
			log.Error("scheduler stopped with error", "err", err)
		}
	}()
	go store.RunRetention(ctx, st, cfg.Retention.RawDays, cfg.Retention.SummaryDays, log)

	webHandler, err := web.New(web.Deps{
		Store:   st,
		Sched:   sched,
		Alerts:  alerts,
		Tracker: tracker,
		Log:     log,
		Secure:  strings.HasPrefix(cfg.Server.BaseURL, "https://"),
	})
	if err != nil {
		return err
	}

	handler := combinedHandler{
		apiHandler: api.New(api.Deps{
			Store:   st,
			Alerts:  alerts,
			Sched:   sched,
			BaseURL: cfg.Server.BaseURL,
			Version: version,
			Log:     log,
		}),
		webHandler: webHandler,
	}

	srv := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Info("tickwatch listening", "addr", cfg.Server.Listen, "db", cfg.Database.Path, "version", version)

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Error("http shutdown incomplete", "err", err)
		}
		return nil
	}
}

func bootstrapAdmin(st store.Store, cfg config.Config, log *slog.Logger) error {
	n, err := st.UserCount()
	if err != nil {
		return fmt.Errorf("count users: %w", err)
	}
	if n > 0 {
		return nil
	}
	password := cfg.Admin.Password
	generated := false
	if password == "" {
		password = store.NewToken(8)
		generated = true
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	user := &store.User{
		Username:     cfg.Admin.Username,
		PasswordHash: string(hash),
		CreatedAt:    time.Now().Unix(),
	}
	if err := st.CreateUser(user); err != nil {
		return fmt.Errorf("create admin user: %w", err)
	}
	if generated {
		log.Warn("no admin user existed; created one with a generated password, change it after first login",
			"username", cfg.Admin.Username, "password", password)
	} else {
		log.Info("created admin user", "username", cfg.Admin.Username)
	}
	return nil
}
