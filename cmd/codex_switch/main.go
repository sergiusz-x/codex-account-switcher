package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"codex-account-switcher/internal/switcher"
	"codex-account-switcher/internal/tui"
	"codex-account-switcher/internal/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "codex_switch:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("codex_switch", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage: codex_switch [--codex-home PATH] [--version]")
		flags.PrintDefaults()
	}

	var codexHome string
	var showVersion bool
	flags.StringVar(&codexHome, "codex-home", "", "Codex home directory (overrides CODEX_HOME)")
	flags.BoolVar(&showVersion, "version", false, "show version")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected argument: %s", flags.Arg(0))
	}
	if showVersion {
		fmt.Println(version.String())
		return nil
	}

	home, err := resolveCodexHome(codexHome, os.Getenv("CODEX_HOME"), os.UserHomeDir)
	if err != nil {
		return err
	}
	store := switcher.NewStore(home)
	return tui.Run(store)
}

func resolveCodexHome(argument, environment string, userHome func() (string, error)) (string, error) {
	path := argument
	if path == "" {
		path = environment
	}
	if path == "" {
		home, err := userHome()
		if err != nil {
			return "", fmt.Errorf("resolve user home directory: %w", err)
		}
		if home == "" {
			return "", errors.New("cannot resolve the home directory; set CODEX_HOME or use --codex-home")
		}
		path = filepath.Join(home, ".codex")
	}

	path, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("invalid Codex home directory: %w", err)
	}
	return filepath.Clean(path), nil
}
