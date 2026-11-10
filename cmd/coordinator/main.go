// Command coordinator runs the Kurwall central management server and its
// administrative CLI.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"golang.org/x/term"

	"github.com/kurwall/kurwall/internal/coordinator/app"
	"github.com/kurwall/kurwall/internal/coordinator/auth"
	"github.com/kurwall/kurwall/internal/coordinator/config"
	"github.com/kurwall/kurwall/internal/coordinator/domain"
	"github.com/kurwall/kurwall/internal/coordinator/store/postgres"
	"github.com/kurwall/kurwall/internal/platform/logging"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "users" {
		if err := runUsers(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "users:", err)
			os.Exit(1)
		}
		return
	}

	path := os.Getenv("APP_CONFIG_PATH")
	flag.StringVar(&path, "config", path, "path to coordinator YAML configuration (defaults to APP_CONFIG_PATH)")
	flag.Parse()
	if path == "" {
		fmt.Fprintln(os.Stderr, "APP_CONFIG_PATH or --config is required")
		os.Exit(2)
	}

	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "configuration error: %v\n", err)
		os.Exit(2)
	}
	logger := logging.New("coordinator", cfg.Log.Level, cfg.Log.Format, os.Stdout)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	application, err := app.New(ctx, cfg, logger)
	if err != nil {
		logger.Error().Err(err).Msg("coordinator startup failed")
		os.Exit(1)
	}
	if err := application.Run(ctx); err != nil {
		logger.Error().Err(err).Msg("coordinator stopped with error")
		os.Exit(1)
	}
}

func runUsers(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: coordinator users create-admin|list|reset-password|disable|enable")
	}
	action := args[0]
	flags := flag.NewFlagSet("users "+action, flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	path := flags.String("config", os.Getenv("APP_CONFIG_PATH"), "path to coordinator YAML configuration")
	username := flags.String("username", "", "account username")
	password := flags.String("password", "", "password (omit to prompt)")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *path == "" {
		return errors.New("APP_CONFIG_PATH or --config is required")
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	store, err := postgres.Open(ctx, cfg.Postgres.DSN(), 2)
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	defer store.Close()

	switch action {
	case "create-admin":
		return createUser(ctx, store, *username, *password, auth.RoleAdmin)
	case "create":
		return createUser(ctx, store, *username, *password, auth.RoleViewer)
	case "list":
		users, err := store.ListUsers(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("%-24s %-8s %-9s %s\n", "USERNAME", "ROLE", "DISABLED", "CREATED")
		for _, user := range users {
			fmt.Printf("%-24s %-8s %-9t %s\n", user.Username, user.Role, user.Disabled,
				user.CreatedAt.Format("2006-01-02 15:04"))
		}
		return nil
	case "reset-password":
		name, err := resolveUsername(*username)
		if err != nil {
			return err
		}
		secret, err := resolvePassword(*password, "New password: ")
		if err != nil {
			return err
		}
		hash, err := auth.HashPassword(secret)
		if err != nil {
			return err
		}
		updated, err := store.UpdateUserPassword(ctx, name, hash)
		if err != nil {
			return err
		}
		if !updated {
			return fmt.Errorf("user %q not found", name)
		}
		fmt.Printf("password updated for %s\n", name)
		return nil
	case "disable", "enable":
		name, err := resolveUsername(*username)
		if err != nil {
			return err
		}
		disabled := action == "disable"
		updated, err := store.SetUserDisabled(ctx, name, disabled)
		if err != nil {
			return err
		}
		if !updated {
			return fmt.Errorf("user %q not found", name)
		}
		fmt.Printf("user %s %sd\n", name, action)
		return nil
	default:
		return fmt.Errorf("unknown action %q", action)
	}
}

func createUser(ctx context.Context, store *postgres.Store, username, password, role string) error {
	name, err := resolveUsername(username)
	if err != nil {
		return err
	}
	if err := auth.ValidateUsername(name); err != nil {
		return err
	}
	secret, err := resolvePassword(password, "Password: ")
	if err != nil {
		return err
	}
	if err := auth.ValidatePassword(secret); err != nil {
		return err
	}
	if password == "" {
		confirmation, err := readPassword("Repeat password: ")
		if err != nil {
			return err
		}
		if confirmation != secret {
			return errors.New("passwords do not match")
		}
	}
	hash, err := auth.HashPassword(secret)
	if err != nil {
		return err
	}
	user, err := store.CreateUser(ctx, domain.User{
		Username: name, PasswordHash: hash, Role: role,
	})
	if err != nil {
		return err
	}
	fmt.Printf("%s user %q created (id %s)\n", user.Role, user.Username, user.ID)
	return nil
}

func resolveUsername(value string) (string, error) {
	name := auth.NormalizeUsername(value)
	if name != "" {
		return name, nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", errors.New("--username is required when stdin is not a terminal")
	}
	fmt.Fprint(os.Stderr, "Username: ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return "", err
	}
	name = auth.NormalizeUsername(line)
	if name == "" {
		return "", errors.New("username is required")
	}
	return name, nil
}

func resolvePassword(value, prompt string) (string, error) {
	if value != "" {
		return value, nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", errors.New("--password is required when stdin is not a terminal")
	}
	fmt.Fprint(os.Stderr, prompt)
	secret, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(secret)) == "" {
		return "", errors.New("password is required")
	}
	return string(secret), nil
}

func readPassword(prompt string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", errors.New("stdin is not a terminal")
	}
	fmt.Fprint(os.Stderr, prompt)
	secret, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	return string(secret), nil
}
