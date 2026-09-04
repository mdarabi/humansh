package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"

	"github.com/agenticlab-ai/humansh/internal/commandcheck"
	usererr "github.com/agenticlab-ai/humansh/internal/errors"
	"github.com/agenticlab-ai/humansh/internal/shell/protocol"
)

const missingExecutablePrefix = "HUMANSH_MISSING:"

var safeExecutableLabel = regexp.MustCompile(`^[A-Za-z0-9_./+@:-]{1,128}$`)

// ExecutableResolution describes a fixed, non-evaluating target-shell command
// that resolves each supplied positional parameter as a command name.
type ExecutableResolution struct {
	ShellName string
	Binary    string
	Args      []string
	Env       []string
}

// ValidateExecutableAvailability resolves only the static command names found
// in command. The generated command itself is never passed to the subprocess.
func ValidateExecutableAvailability(ctx context.Context, command string, resolution ExecutableResolution) error {
	names := commandcheck.ExecutableNames(command)
	if len(names) == 0 {
		return nil
	}

	args := make([]string, 0, len(resolution.Args)+len(names))
	args = append(args, resolution.Args...)
	args = append(args, names...)
	check := exec.CommandContext(ctx, resolution.Binary, args...)
	check.Env = append([]string(nil), resolution.Env...)
	check.Stderr = io.Discard
	output, err := check.Output()
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return usererr.WithExit(protocol.ExitProviderMalformed, "executable_validation_timeout", "Generated command availability validation timed out.", "Nothing was changed or executed.", true, ctx.Err())
	}
	if missing, ok := missingExecutable(output, names, err); ok {
		summary := "Your original text is unchanged. Install the required tool or ask for a command that uses a different one."
		if safeExecutableLabel.MatchString(missing) {
			summary = fmt.Sprintf("%q is not available to %s. Install it or ask for a command that uses a different tool; your original text is unchanged.", missing, resolution.ShellName)
		}
		return usererr.WithExit(protocol.ExitProviderMalformed, "generated_command_unavailable", "Generated command requires an unavailable executable.", summary, true, fmt.Errorf("%s could not resolve executable %q", resolution.ShellName, missing))
	}
	return usererr.WithExit(protocol.ExitProviderMalformed, "executable_validation_failed", "Generated command availability could not be checked safely.", "Nothing was changed or executed.", true, fmt.Errorf("%s executable resolution failed: %w", resolution.ShellName, err))
}

func missingExecutable(output []byte, names []string, runErr error) (string, bool) {
	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) || exitErr.ExitCode() != 127 {
		return "", false
	}
	line := strings.TrimSuffix(string(output), "\n")
	missing, ok := strings.CutPrefix(line, missingExecutablePrefix)
	if !ok {
		return "", false
	}
	for _, name := range names {
		if missing == name {
			return missing, true
		}
	}
	return "", false
}
