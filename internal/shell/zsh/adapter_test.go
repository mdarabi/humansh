package zsh

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	usererr "github.com/agenticlab-ai/humansh/internal/errors"
	"github.com/agenticlab-ai/humansh/internal/shell"
	"github.com/agenticlab-ai/humansh/internal/shell/contracttest"
	"github.com/agenticlab-ai/humansh/internal/shell/protocol"
)

func TestSyntaxCheckDoesNotExecute(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	marker := filepath.Join(dir, "pwned")
	command := "echo $(touch " + marker + ") > " + filepath.Join(dir, "output")
	if err := (Adapter{}).ValidateGenerated(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("syntax check executed command: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "output")); !os.IsNotExist(err) {
		t.Fatal("syntax check performed redirection")
	}
}

func TestSyntaxError(t *testing.T) {
	t.Parallel()
	if err := (Adapter{}).ValidateGenerated(context.Background(), "if then"); err == nil {
		t.Fatal("invalid syntax accepted")
	}
}

func TestAvailabilityCheckUsesTheLocalPathWithoutExecutingTheCommand(t *testing.T) {
	shellBinary, err := exec.LookPath("zsh")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	tool := filepath.Join(bin, "humansh-test-optional-zsh")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nprintf executed > \"$0.executed\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	adapter := Adapter{Binary: shellBinary}
	if err := adapter.ValidateGenerated(context.Background(), "humansh-test-optional-zsh --version"); err != nil {
		t.Fatalf("arbitrary PATH executable was rejected: %v", err)
	}
	if _, err := os.Stat(tool + ".executed"); !os.IsNotExist(err) {
		t.Fatalf("availability check executed the command: %v", err)
	}
	if err := adapter.ValidateGenerated(context.Background(), "printf '%s\\n' builtin"); err != nil {
		t.Fatalf("Zsh builtin was rejected: %v", err)
	}

	err = adapter.ValidateGenerated(context.Background(), "humansh-test-missing-zsh --version")
	typed, ok := usererr.As(err)
	if !ok || typed.Code != "generated_command_unavailable" || typed.ExitCode != protocol.ExitProviderMalformed {
		t.Fatalf("missing executable error=%#v", err)
	}
}

func TestShellContract(t *testing.T) {
	t.Parallel()
	contracttest.Run(t, Adapter{}, shell.Zsh, protocol.Version, true)
}
