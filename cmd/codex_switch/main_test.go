package main

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestResolveCodexHomePrecedence(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home", "tester")
	userHome := func() (string, error) { return home, nil }
	argument := filepath.Join(root, "explicit")
	environment := filepath.Join(root, "environment")
	absolute := func(path string) string {
		value, err := filepath.Abs(path)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	for _, test := range []struct {
		name, argument, environment, want string
	}{
		{name: "argument", argument: argument, environment: environment, want: absolute(argument)},
		{name: "environment", environment: environment, want: absolute(environment)},
		{name: "default", want: absolute(filepath.Join(home, ".codex"))},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveCodexHome(test.argument, test.environment, userHome)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("resolveCodexHome() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestResolveCodexHomeRequiresHomeWhenUnset(t *testing.T) {
	got, err := resolveCodexHome("", "", func() (string, error) { return "", errors.New("no home") })
	if err == nil || got != "" {
		t.Fatalf("resolveCodexHome() = %q, %v; want error", got, err)
	}
}

func TestHelpReturnsSuccess(t *testing.T) {
	if err := run([]string{"--help"}); err != nil {
		t.Fatalf("run(--help) error = %v, want nil", err)
	}
}
