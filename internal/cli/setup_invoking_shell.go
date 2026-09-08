package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/agenticlab-ai/humansh/internal/shell"
)

const maxInvokingShellDepth = 16

type parentProcessLookup func(pid int) (parentPID int, command string, err error)

func detectInvokingShell() shell.ID {
	psPath := systemPSPath()
	if psPath == "" {
		return ""
	}
	return invokingShellFromAncestors(os.Getppid(), func(pid int) (int, string, error) {
		output, err := exec.Command(psPath, "-p", strconv.Itoa(pid), "-o", "ppid=", "-o", "args=").Output()
		if err != nil {
			return 0, "", err
		}
		return parseParentProcess(output)
	})
}

func systemPSPath() string {
	for _, candidate := range []string{"/bin/ps", "/usr/bin/ps"} {
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return candidate
		}
	}
	return ""
}

func invokingShellFromAncestors(startPID int, lookup parentProcessLookup) shell.ID {
	pid := startPID
	seen := make(map[int]struct{}, maxInvokingShellDepth)
	for depth := 0; depth < maxInvokingShellDepth && pid > 1; depth++ {
		if _, duplicate := seen[pid]; duplicate {
			return ""
		}
		seen[pid] = struct{}{}
		parentPID, command, err := lookup(pid)
		if err != nil {
			return ""
		}
		if id := processShellID(command); id != "" {
			return id
		}
		if parentPID <= 1 || parentPID == pid {
			return ""
		}
		pid = parentPID
	}
	return ""
}

func parseParentProcess(output []byte) (int, string, error) {
	line := strings.TrimSpace(string(output))
	separator := strings.IndexAny(line, " \t")
	if separator <= 0 {
		return 0, "", fmt.Errorf("unexpected ps output")
	}
	parentPID, err := strconv.Atoi(line[:separator])
	if err != nil {
		return 0, "", fmt.Errorf("parse parent pid: %w", err)
	}
	command := strings.TrimSpace(line[separator:])
	if command == "" || strings.ContainsAny(command, "\r\n") {
		return 0, "", fmt.Errorf("unexpected ps command")
	}
	return parentPID, command, nil
}

func processShellID(command string) shell.ID {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ""
	}
	executable := strings.Trim(fields[0], `"'`)
	name := strings.TrimPrefix(filepath.Base(executable), "-")
	switch name {
	case string(shell.Bash):
		return shell.Bash
	case string(shell.Zsh):
		return shell.Zsh
	default:
		return ""
	}
}
