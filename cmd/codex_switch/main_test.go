package main

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestResolveCodexHomePrecedence(t *testing.T) {
	userHome := func() (string, error) { return "/home/tester", nil }
	for _, test := range []struct {
		name, argument, environment, want string
	}{
		{name: "argument", argument: "/tmp/explicit", environment: "/tmp/environment", want: "/tmp/explicit"},
		{name: "environment", environment: "/tmp/environment", want: "/tmp/environment"},
		{name: "default", want: filepath.Join("/home/tester", ".codex")},
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
