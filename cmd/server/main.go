package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/Druckkette/Wedding/internal/app"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		client := http.Client{Timeout: 3 * time.Second}
		response, err := client.Get("http://127.0.0.1:8080/healthz")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			fmt.Fprintf(os.Stderr, "unexpected health status: %s\n", response.Status)
			os.Exit(1)
		}
		return
	}

	cfg, err := app.ConfigFromEnv()
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}

	handler, err := app.New(cfg)
	if err != nil {
		log.Fatalf("startup error: %v", err)
	}

	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    16 << 10,
	}

	log.Printf("wedding upload server listening on %s", cfg.ListenAddr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("server stopped: %v", err)
		os.Exit(1)
	}
}
