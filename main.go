package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/besoeasy/originless/internal/config"
	"github.com/besoeasy/originless/internal/ipfs"
	"github.com/besoeasy/originless/internal/server"
)

const defaultPort = "3232"

var version = "dev"

func configuredPort() (string, error) {
	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		return defaultPort, nil
	}

	value, err := strconv.Atoi(port)
	if err != nil || value < 1 || value > 65535 {
		return "", fmt.Errorf("invalid PORT %q: must be between 1 and 65535", port)
	}
	return strconv.Itoa(value), nil
}

func main() {
	port, err := configuredPort()
	if err != nil {
		log.Fatal(err)
	}

	cfg, err := config.FromEnv(os.LookupEnv)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("originless: settings %s", cfg.Describe())

	client, err := ipfs.NewClient("http://127.0.0.1:5001")
	if err != nil {
		log.Fatal(err)
	}

	// The reaper and any other background work stop when this context is done.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	handler, err := server.NewRouterWithOptions(ctx, client, cfg)
	if err != nil {
		log.Fatal(err)
	}
	httpServer := &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       0,
		WriteTimeout:      0,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 16,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			log.Printf("HTTP shutdown failed: %v", err)
		}
	}()

	log.Printf("Originless %s listening on :%s", version, port)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
