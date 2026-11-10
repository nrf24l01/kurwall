// Command agent runs the Kurwall node agent on a Linux server.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/kurwall/kurwall/internal/agent/app"
	"github.com/kurwall/kurwall/internal/agent/config"
	"github.com/kurwall/kurwall/internal/platform/logging"
	"github.com/kurwall/kurwall/internal/platform/version"
)

func main() {
	path := os.Getenv("APP_CONFIG_PATH")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.StringVar(&path, "config", path, "path to agent YAML configuration (defaults to APP_CONFIG_PATH)")
	flag.Parse()
	if *showVersion {
		fmt.Println(version.Version)
		return
	}
	if path == "" {
		fmt.Fprintln(os.Stderr, "APP_CONFIG_PATH or --config is required")
		os.Exit(2)
	}

	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "configuration error: %v\n", err)
		os.Exit(2)
	}
	logger := logging.New("agent", cfg.Log.Level, cfg.Log.Format, os.Stdout)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	application, err := app.New(ctx, cfg, logger)
	if err != nil {
		logger.Error().Err(err).Msg("agent startup failed")
		os.Exit(1)
	}
	if err := application.Run(ctx); err != nil {
		logger.Error().Err(err).Msg("agent stopped with error")
		os.Exit(1)
	}
}
