package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"

	"liga-discord-gc/internal/api"
	"liga-discord-gc/internal/app"
	"liga-discord-gc/internal/config"
	"liga-discord-gc/internal/dota"
	steamclient "liga-discord-gc/internal/steam"
)

func main() {
	logger := newLogger()

	logger.Info("═══════════════════════════════════════════")
	logger.Info("  Liga Fumos — Dota 2 GC Service")
	logger.Info("═══════════════════════════════════════════")

	// ── 1. Configuração ──────────────────────────────
	logger.Info("[Boot] Carregando configuração...")
	cfg, err := config.Load()
	if err != nil {
		logger.WithError(err).Fatal("[Boot] Configuração inválida")
	}
	logger.WithFields(logrus.Fields{
		"username":  cfg.SteamUsername,
		"http_port": cfg.HTTPPort,
		"has_2fa":   cfg.Steam2FACode != "",
	}).Info("[Boot] Configuração carregada")

	// ── 2. Estado da aplicação ───────────────────────
	logger.Info("[Boot] Inicializando estado da aplicação...")
	a := app.New()

	// ── 3. Cliente Steam ──────────────────────────────
	logger.Info("[Boot] Criando cliente Steam...")
	steam := steamclient.New(cfg, a, logger, func(d *dota.Client) {
		logger.Info("[Boot] GC pronto — serviço está operacional")
		_ = d
	})

	// ── 4. Servidor HTTP ──────────────────────────────
	logger.WithField("port", cfg.HTTPPort).Info("[Boot] Criando servidor HTTP da API...")
	srv := api.New(cfg.HTTPPort, a, steam.GetDotaClient, logger, cfg.SteamAPIKey)

	// ── 5. Inicia o servidor HTTP em background ───────
	go srv.Start()

	// ── 6. Conecta ao Steam e entra no loop de eventos ─
	steam.Connect()

	logger.Info("[Boot] Tudo iniciado — aguardando SIGINT/SIGTERM para encerrar")

	// ── 7. Aguarda sinal de shutdown ──────────────────
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	// O loop de eventos bloqueia até a conexão fechar;
	// rodamos em goroutine para poder processar o stop.
	done := make(chan struct{})
	go func() {
		steam.RunEventLoop()
		close(done)
	}()

	select {
	case sig := <-stop:
		logger.WithField("signal", sig.String()).Info("[Boot] Sinal recebido — iniciando shutdown gracioso...")
	case <-done:
		logger.Warn("[Boot] Loop de eventos encerrado inesperadamente")
	}

	// ── 8. Shutdown gracioso ──────────────────────────
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer shutCancel()

	logger.Info("[Boot] Encerrando servidor HTTP...")
	srv.Shutdown(shutCtx)

	logger.Info("[Boot] Desconectando do Steam...")
	steam.Disconnect()

	logger.Info("[Boot] Encerrado com sucesso")
}

func newLogger() *logrus.Logger {
	l := logrus.New()
	l.SetFormatter(&logrus.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: "2006-01-02 15:04:05",
	})
	l.SetOutput(os.Stdout)

	level := os.Getenv("LOG_LEVEL")
	switch level {
	case "debug":
		l.SetLevel(logrus.DebugLevel)
	case "warn":
		l.SetLevel(logrus.WarnLevel)
	default:
		l.SetLevel(logrus.InfoLevel)
	}

	return l
}
