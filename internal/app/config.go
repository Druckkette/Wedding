package app

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddr        string
	UploadDir         string
	UploadToken       string
	EventTitle        string
	EventSubtitle     string
	EventTimezone     string
	SettingsPassword  string
	MaxUploadsPerHour int
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
}

func ConfigFromEnv() (Config, error) {
	cfg := Config{
		ListenAddr:        envOr("LISTEN_ADDR", ":8080"),
		UploadDir:         envOr("UPLOAD_DIR", "/data/uploads"),
		UploadToken:       strings.TrimSpace(os.Getenv("UPLOAD_TOKEN")),
		EventTitle:        envOr("EVENT_TITLE", "Unsere Hochzeit"),
		EventSubtitle:     envOr("EVENT_SUBTITLE", "Haltet eure schönsten Momente mit uns fest."),
		EventTimezone:     envOr("EVENT_TIMEZONE", "Europe/Berlin"),
		SettingsPassword:  strings.TrimSpace(os.Getenv("SETTINGS_PASSWORD")),
		MaxUploadsPerHour: 120,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       0,
		WriteTimeout:      0,
		IdleTimeout:       2 * time.Minute,
	}

	if value := strings.TrimSpace(os.Getenv("MAX_UPLOADS_PER_HOUR")); value != "" {
		limit, err := strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 5000 {
			return Config{}, fmt.Errorf("MAX_UPLOADS_PER_HOUR must be between 1 and 5000")
		}
		cfg.MaxUploadsPerHour = limit
	}

	if len(cfg.UploadToken) < 32 {
		return Config{}, errors.New("UPLOAD_TOKEN must contain at least 32 characters")
	}
	if strings.ContainsAny(cfg.UploadToken, "/?#") {
		return Config{}, errors.New("UPLOAD_TOKEN must be URL-path safe")
	}
	if len(cfg.SettingsPassword) < 6 || len(cfg.SettingsPassword) > 128 {
		return Config{}, errors.New("SETTINGS_PASSWORD must contain between 6 and 128 characters")
	}
	if _, err := time.LoadLocation(cfg.EventTimezone); err != nil {
		return Config{}, fmt.Errorf("EVENT_TIMEZONE is invalid: %w", err)
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
