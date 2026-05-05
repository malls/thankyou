// Command server starts the Thank You HTTP server.
//
// Static site assets, the SVG render template, and the woff font are all
// baked into the binary at compile time, so the resulting binary is
// self-contained: copy it onto a host, run it, done.
//
// Environment variables:
//
//	PORT       — listen port; defaults to 8080.
//	DATA_DIR   — directory for saved PNGs; defaults to ./data/files.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/forrestalmasi/thankyou/server/internal/files"
	"github.com/forrestalmasi/thankyou/server/internal/httpserver"
	"github.com/forrestalmasi/thankyou/server/internal/render"
)

func main() {
	logger := log.New(os.Stdout, "thankyou: ", log.LstdFlags|log.Lmicroseconds)

	port := envOr("PORT", "8080")
	dataDir := envOr("DATA_DIR", "./data/files")

	store, err := files.New(dataDir)
	if err != nil {
		logger.Fatalf("init file store: %v", err)
	}
	logger.Printf("file store rooted at %s", store.Dir())

	// Boot the renderer up front so font-load failures are fatal at startup
	// rather than per-request 500s. The wasm runtime takes ~50ms to spin up.
	renderer, err := render.NewRenderer(context.Background())
	if err != nil {
		logger.Fatalf("init renderer: %v", err)
	}
	defer func() {
		if err := renderer.Close(); err != nil {
			logger.Printf("renderer close: %v", err)
		}
	}()

	handler, err := httpserver.NewRouter(&httpserver.Handlers{
		Renderer: renderer,
		Store:    store,
		Logger:   logger,
	})
	if err != nil {
		logger.Fatalf("init router: %v", err)
	}

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		// Renders take a couple seconds; allow generous totals.
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Listen-and-serve in its own goroutine so we can intercept signals
	// and shut down cleanly. Without this, a Ctrl-C would kill in-flight
	// renders mid-write and leave .tmp files lying around.
	errCh := make(chan error, 1)
	go func() {
		logger.Printf("listening on http://localhost:%s", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		logger.Printf("received %s, shutting down", sig)
	case err := <-errCh:
		logger.Printf("server error: %v", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Printf("shutdown: %v", err)
	}
}

// envOr returns the named env var or `fallback` when the var is unset/empty.
// One-liner wrapper purely for readability at the call site.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
