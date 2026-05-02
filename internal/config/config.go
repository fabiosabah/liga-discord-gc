package config

import (
	"fmt"
	"os"
)

type Config struct {
	SteamUsername string
	SteamPassword string
	Steam2FACode  string
	HTTPPort      string
	SteamAPIKey   string
}

func Load() (*Config, error) {
	port := os.Getenv("GC_HTTP_PORT")
	if port == "" {
		port = "8080"
	}

	cfg := &Config{
		SteamUsername: os.Getenv("STEAM_USERNAME"),
		SteamPassword: os.Getenv("STEAM_PASSWORD"),
		Steam2FACode:  os.Getenv("STEAM_2FA_CODE"),
		HTTPPort:      port,
		SteamAPIKey:   os.Getenv("STEAM_API_KEY"),
	}

	if cfg.SteamUsername == "" || cfg.SteamPassword == "" {
		return nil, fmt.Errorf("STEAM_USERNAME e STEAM_PASSWORD são obrigatórios")
	}

	return cfg, nil
}
