package cli

import (
	"fmt"
	"testing"

	"github.com/agenticlab-ai/humansh/internal/config"
	"github.com/agenticlab-ai/humansh/internal/shell"
)

func TestInvokingShellUsesNearestSupportedAncestor(t *testing.T) {
	t.Parallel()
	processes := map[int]struct {
		parent  int
		command string
	}{
		50: {parent: 40, command: "/bin/sh"},
		40: {parent: 30, command: "make"},
		30: {parent: 20, command: "/opt/homebrew/bin/bash"},
		20: {parent: 1, command: "/bin/zsh"},
	}
	got := invokingShellFromAncestors(50, func(pid int) (int, string, error) {
		process, ok := processes[pid]
		if !ok {
			return 0, "", fmt.Errorf("unknown pid %d", pid)
		}
		return process.parent, process.command, nil
	})
	if got != shell.Bash {
		t.Fatalf("invoking shell=%q, want bash", got)
	}
}

func TestQuickSetupPrefersInvokingShellOverLoginShell(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")
	shells, verify, fallback, err := quickSetupShells(setupOptions{}, config.Default(), config.InstallState{}, false, shell.Bash, false)
	if err != nil || len(shells) != 1 || shells[0] != shell.Bash || !verify || fallback {
		t.Fatalf("shells=%v verify=%t fallback=%t err=%v", shells, verify, fallback, err)
	}
}

func TestInstallerQuickSetupAddsInvokingShellToInstallState(t *testing.T) {
	t.Parallel()
	state := config.InstallState{Integrations: []config.ShellInstallState{{Shell: string(shell.Bash)}}}
	shells, verify, fallback, err := quickSetupShells(setupOptions{}, config.Default(), state, true, shell.Zsh, true)
	if err != nil || len(shells) != 2 || shells[0] != shell.Zsh || shells[1] != shell.Bash || !verify || fallback {
		t.Fatalf("shells=%v verify=%t fallback=%t err=%v", shells, verify, fallback, err)
	}
}

func TestParseParentProcess(t *testing.T) {
	t.Parallel()
	parent, command, err := parseParentProcess([]byte("  123 /opt/homebrew/bin/bash\n"))
	if err != nil || parent != 123 || command != "/opt/homebrew/bin/bash" {
		t.Fatalf("parent=%d command=%q err=%v", parent, command, err)
	}
}
