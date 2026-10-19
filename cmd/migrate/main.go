package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/KevVemn/junglegaming-backend/internal/config"
	"github.com/KevVemn/junglegaming-backend/internal/postgres"
	"github.com/KevVemn/junglegaming-backend/migrations"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	command := "up"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, err := postgres.NewPool(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer pool.Close()

	migrator, err := postgres.NewMigrator(pool, migrations.FS)
	if err != nil {
		return err
	}

	switch command {
	case "up":
		executed, err := migrator.Up(ctx)
		if err != nil {
			return err
		}
		if len(executed) == 0 {
			fmt.Println("no pending migrations")
			return nil
		}
		for _, name := range executed {
			fmt.Printf("applied %s\n", name)
		}
		return nil

	case "down":
		reverted, err := migrator.Down(ctx)
		if err != nil {
			return err
		}
		if reverted == "" {
			fmt.Println("nothing to revert")
			return nil
		}
		fmt.Printf("reverted %s\n", reverted)
		return nil

	case "status":
		status, err := migrator.Status(ctx)
		if err != nil {
			return err
		}
		for _, line := range status {
			fmt.Println(line)
		}
		return nil

	default:
		return fmt.Errorf("unknown command %q (use: up, down, status)", command)
	}
}
