package zsh

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/agenticlab-ai/humansh/assets"
	usererr "github.com/agenticlab-ai/humansh/internal/errors"
	"github.com/agenticlab-ai/humansh/internal/processrunner"
	"github.com/agenticlab-ai/humansh/internal/shell"
	"github.com/agenticlab-ai/humansh/internal/shell/protocol"
)

type Adapter struct {
	Binary string
	Runner processrunner.Runner
}

func (Adapter) ID() shell.ID { return shell.Zsh }
func (a Adapter) Diagnose(ctx context.Context) shell.Diagnostic {
	diagnosticCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	tempDir, err := os.MkdirTemp("", "humansh-zsh-diagnose-*")
	if err != nil {
		return shell.Diagnostic{Message: "Zsh diagnostic isolation could not be created"}
	}
	defer os.RemoveAll(tempDir)
	if err := os.Chmod(tempDir, 0o700); err != nil {
		return shell.Diagnostic{Message: "Zsh diagnostic isolation could not be secured"}
	}
	result, err := a.runner().Run(diagnosticCtx, processrunner.Spec{
		Path: a.binary(), Args: []string{"--version"}, Dir: tempDir,
		Env: []string{"HOME=" + tempDir, "LANG=C", "PATH=/usr/bin:/bin", "TMPDIR=" + tempDir}, MaxStdout: 4096, MaxStderr: 4096,
	})
	if processrunner.IsNotFound(err) {
		return shell.Diagnostic{Message: "Zsh is not installed or is absent from PATH"}
	}
	if err != nil {
		return shell.Diagnostic{Installed: true, Message: "Zsh version check failed"}
	}
	version := strings.TrimSpace(string(result.Stdout))
	if version == "" || !strings.HasPrefix(strings.ToLower(version), "zsh ") {
		return shell.Diagnostic{Installed: true, Version: version, Message: "Zsh returned an unrecognized version response"}
	}
	return shell.Diagnostic{Installed: true, Available: true, Version: version, Message: "ZLE integration is supported"}
}
func (Adapter) Capabilities() shell.Capabilities {
	return shell.Capabilities{InspectEditableBuffer: true, ReplaceEditableBuffer: true, ConditionalAccept: true, ResolveAliases: true, ResolveFunctions: true, ExplicitPrefixMode: true}
}
func (Adapter) PromptProfile() shell.PromptProfile { return shell.PromptProfile{Shell: "zsh"} }
func (Adapter) NormalizeGenerated(command string) (string, error) {
	return strings.TrimSpace(command), nil
}
func (Adapter) IntegrationAsset() ([]byte, bool) {
	return append([]byte(nil), assets.ZshIntegration...), true
}
func (Adapter) SupportedProtocols() []string { return []string{protocol.Version} }

func (a Adapter) ValidateGenerated(ctx context.Context, command string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	tempDir, err := os.MkdirTemp("", "humansh-zsh-validate-*")
	if err != nil {
		return usererr.WithExit(protocol.ExitProviderMalformed, "syntax_environment", "Generated command syntax could not be checked safely.", "Nothing was changed or executed.", true, err)
	}
	defer os.RemoveAll(tempDir)
	if err := os.Chmod(tempDir, 0o700); err != nil {
		return usererr.WithExit(protocol.ExitProviderMalformed, "syntax_environment", "Generated command syntax could not be checked safely.", "Nothing was changed or executed.", true, err)
	}
	cmd := exec.CommandContext(ctx, a.binary(), "-f", "-n")
	cmd.Dir = tempDir
	cmd.Env = []string{"HOME=" + tempDir, "LANG=C", "PATH=/usr/bin:/bin", "TMPDIR=" + tempDir}
	cmd.Stdin = strings.NewReader(command)
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return usererr.WithExit(protocol.ExitProviderMalformed, "syntax_timeout", "Generated command syntax validation timed out.", "Nothing was changed or executed.", true, ctx.Err())
		}
		return usererr.WithExit(protocol.ExitProviderMalformed, "syntax_invalid", "Provider returned a command that is not valid Zsh syntax.", "Nothing was changed or executed.", true, fmt.Errorf("zsh syntax check failed: %w", err))
	}
	pathValue := os.Getenv("PATH")
	if pathValue == "" {
		pathValue = "/usr/bin:/bin"
	}
	const resolveScript = `for name in "$@"; do
  if ! whence -w -- "$name" >/dev/null 2>&1; then
    print -r -- "HUMANSH_MISSING:$name"
    exit 127
  fi
done`
	return shell.ValidateExecutableAvailability(ctx, command, shell.ExecutableResolution{
		ShellName: "Zsh",
		Binary:    a.binary(),
		Args:      []string{"-f", "-c", resolveScript, "humansh-executable-check"},
		Env:       []string{"HOME=" + tempDir, "LANG=C", "PATH=" + pathValue, "TMPDIR=" + tempDir},
	})
}

func (a Adapter) binary() string {
	if a.Binary != "" {
		return a.Binary
	}
	return "zsh"
}

func (a Adapter) runner() processrunner.Runner {
	if a.Runner != nil {
		return a.Runner
	}
	return processrunner.ExecRunner{}
}
