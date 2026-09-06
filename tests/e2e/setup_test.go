package e2e_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSetupFlowIsConciseAndHealthyRerunIsSilent is the end-to-end regression
// for the original setup feedback: a first install used to walk through six
// verbose sections and then launch another interactive onboarding guide.
func TestSetupFlowIsConciseAndHealthyRerunIsSilent(t *testing.T) {
	if os.Getenv("HUMANSH_RUN_E2E") != "1" {
		t.Skip("set HUMANSH_RUN_E2E=1 to run the installed-flow tests")
	}
	for _, command := range []string{"go", "zsh"} {
		if _, err := exec.LookPath(command); err != nil {
			t.Fatalf("required E2E command %q is unavailable: %v", command, err)
		}
	}

	repo := repositoryRoot(t)
	root := t.TempDir()
	home := filepath.Join(root, "home")
	providerBin := filepath.Join(root, "bin")
	for _, directory := range []string{home, providerBin} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	binary := filepath.Join(root, "humansh")
	build := exec.Command("go", "build", "-trimpath", "-o", binary, "./cmd/humansh")
	build.Dir = repo
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build Humansh: %v\n%s", err, output)
	}
	fakeCodex := filepath.Join(providerBin, "codex")
	build = exec.Command("go", "build", "-trimpath", "-o", fakeCodex, "./tests/e2e/testdata/fakecodex")
	build.Dir = repo
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build deterministic Codex fixture: %v\n%s", err, output)
	}
	for _, name := range []string{"claude", "cursor-agent"} {
		if err := os.Symlink(fakeCodex, filepath.Join(providerBin, name)); err != nil {
			t.Fatal(err)
		}
	}

	probeLog := filepath.Join(providerBin, "humansh-e2e-probes.log")
	env := setEnvironment(os.Environ(),
		"HOME", home,
		"ZDOTDIR", home,
		"XDG_CONFIG_HOME", filepath.Join(home, "config"),
		"XDG_DATA_HOME", filepath.Join(home, "data"),
		"XDG_CACHE_HOME", filepath.Join(home, "cache"),
		"CODEX_HOME", filepath.Join(home, "codex"),
		"OPENROUTER_API_KEY", "",
		"SHELL", "/bin/zsh",
		"NO_COLOR", "1",
		"TERM", "dumb",
		"PATH", providerBin+string(os.PathListSeparator)+"/usr/bin:/bin",
	)

	first := runSetupInPTY(t, binary, env, true, "🎉 Humansh is ready!")
	if count := strings.Count(first, "Continue? [Y/n]:"); count != 1 {
		t.Fatalf("first setup confirmation count=%d, want 1:\n%s", count, first)
	}
	if count := strings.Count(first, "AI provider [1]:"); count != 1 {
		t.Fatalf("first setup provider-choice count=%d, want 1:\n%s", count, first)
	}
	for _, want := range []string{
		"Choose your AI provider",
		"Review",
		"  Shell      Zsh",
		"  Provider   Codex",
		"  Startup    Update ~/.zshrc",
		"  Safety     Commands wait for your review",
		"  ✓ Codex ready",
		"🎉 Humansh is ready!",
		"  Settings   `humansh setup --advanced`",
		"  Try it: open a new terminal, type `list files`, press Enter to translate, then Enter to run.",
	} {
		if !strings.Contains(first, want) {
			t.Errorf("first setup missing %q:\n%s", want, first)
		}
	}
	for _, unwanted := range []string{"1/6", "Translation preferences", "Shell controls", "Shell activation patch", "Apply this setup?", "Getting started with Humansh", "Show the Bash walkthrough", "More than one AI provider is installed:", "Humansh found:", "Humansh will", "Generated commands are always shown", "Customize:", "Controls   ", "One small Codex request"} {
		if strings.Contains(first, unwanted) {
			t.Errorf("first setup included verbose flow marker %q:\n%s", unwanted, first)
		}
	}
	previous := -1
	for _, marker := range []string{"Choose your AI provider", "Review", "Continue? [Y/n]:", "✓ Codex ready", "🎉 Humansh is ready!"} {
		index := strings.Index(first, marker)
		if index <= previous {
			t.Fatalf("first setup sections are missing or out of order at %q:\n%s", marker, first)
		}
		previous = index
	}
	if lines := nonemptySetupLines(first); lines > 20 {
		t.Fatalf("first setup printed %d non-empty lines, want at most 20:\n%s", lines, first)
	}
	if probes := setupProbeCount(t, probeLog); probes != 1 {
		t.Fatalf("first setup provider probes=%d, want 1", probes)
	}
	zshrc := filepath.Join(home, ".zshrc")
	beforeStartup, err := os.ReadFile(zshrc)
	if err != nil || !strings.Contains(string(beforeStartup), "# >>> humansh >>>") {
		t.Fatalf("first setup did not activate Zsh: err=%v\n%s", err, beforeStartup)
	}
	if _, err := os.Stat(filepath.Join(home, ".bashrc")); !os.IsNotExist(err) {
		t.Fatalf("first setup unexpectedly activated Bash: %v", err)
	}
	configPath := filepath.Join(home, "config", "humansh", "config.toml")
	beforeConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	second := runSetupInPTY(t, binary, env, false, "Existing setup kept")
	if strings.Contains(second, "[Y/n]") || strings.Contains(second, "AI provider [") {
		t.Fatalf("healthy setup rerun prompted for input:\n%s", second)
	}
	for _, want := range []string{"  ✓ Existing setup kept", "    Shells     Zsh", "    Provider   Codex", "    Settings   Change with `humansh setup --advanced`"} {
		if !strings.Contains(second, want) {
			t.Errorf("healthy setup rerun missing aligned result %q:\n%s", want, second)
		}
	}
	if strings.Contains(second, "already set up") {
		t.Fatalf("healthy setup rerun used fresh-setup wording:\n%s", second)
	}
	if probes := setupProbeCount(t, probeLog); probes != 1 {
		t.Fatalf("healthy setup rerun made another provider probe; total=%d", probes)
	}
	afterConfig, err := os.ReadFile(configPath)
	if err != nil || string(afterConfig) != string(beforeConfig) {
		t.Fatalf("healthy setup rerun changed configuration: err=%v\nbefore=%s\nafter=%s", err, beforeConfig, afterConfig)
	}
	afterStartup, err := os.ReadFile(zshrc)
	if err != nil || string(afterStartup) != string(beforeStartup) {
		t.Fatalf("healthy setup rerun changed .zshrc: err=%v\nbefore=%s\nafter=%s", err, beforeStartup, afterStartup)
	}
}

// TestFreshInstallStopsCleanlyWhenProviderCheckFails is the end-to-end
// regression for a fresh `make install` with a signed-out Cursor CLI. The old
// flow interpreted Cursor's wording, prescribed a login command, and leaked
// exit 22 through Make as `make: *** [install] Error 22`.
func TestFreshInstallStopsCleanlyWhenProviderCheckFails(t *testing.T) {
	if os.Getenv("HUMANSH_RUN_E2E") != "1" {
		t.Skip("set HUMANSH_RUN_E2E=1 to run the installed-flow tests")
	}
	for _, command := range []string{"go", "zsh"} {
		if _, err := exec.LookPath(command); err != nil {
			t.Fatalf("required E2E command %q is unavailable: %v", command, err)
		}
	}

	repo := repositoryRoot(t)
	root := t.TempDir()
	home := filepath.Join(root, "home")
	providerBin := filepath.Join(root, "providers")
	for _, directory := range []string{home, providerBin} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"codex", "claude"} {
		if err := os.WriteFile(filepath.Join(providerBin, name), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cursorFailure := "#!/bin/sh\nprintf '%s\\n' \"Error: Authentication required. Please run 'agent login' first.\" >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(providerBin, "cursor-agent"), []byte(cursorFailure), 0o700); err != nil {
		t.Fatal(err)
	}

	wrapper := filepath.Join(root, "run-installer")
	wrapperScript := "#!/bin/sh\nmake install\nstatus=$?\nprintf 'INSTALL_STATUS:%s\\n' \"$status\"\nexit 0\n"
	if err := os.WriteFile(wrapper, []byte(wrapperScript), 0o700); err != nil {
		t.Fatal(err)
	}
	env := setEnvironment(isolatedEnvironment(t, home, providerBin),
		"HUMANSH_NONINTERACTIVE", "0",
		"SHELL", "/bin/zsh",
		"NO_COLOR", "1",
		"TERM", "dumb",
	)

	const script = `zmodload zsh/zpty || exit 90
zpty -b I "$HUMANSH_E2E_WRAPPER"
seen=''
provider_answered=0
confirmed=0
for attempt in {1..2000}; do
  while zpty -r -t I chunk; do seen+=$chunk; done
  if [[ $seen == *'AI provider [1]:'* && $provider_answered -eq 0 ]]; then
    zpty -w -n I $'3\r'
    provider_answered=1
  fi
  if [[ $seen == *'Continue? [Y/n]:'* && $confirmed -eq 0 ]]; then
    zpty -w -n I $'\r'
    confirmed=1
  fi
  if [[ $seen == *'INSTALL_STATUS:0'* ]]; then
    while zpty -r -t I chunk; do seen+=$chunk; done
    print -r -- "$seen"
    zpty -d I
    exit 0
  fi
  sleep 0.01
done
print -ru2 -- "installer did not finish: ${(V)seen}"
zpty -d I
exit 91`
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "zsh", "-f", "-c", script)
	command.Dir = repo
	command.Env = setEnvironment(env, "HUMANSH_E2E_WRAPPER", wrapper)
	output, err := command.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			t.Fatalf("logged-out Cursor install exceeded its timeout:\n%s", output)
		}
		t.Fatalf("logged-out Cursor install failed in its PTY wrapper: %v\n%s", err, output)
	}
	text := string(output)
	for _, want := range []string{
		"Cursor CLI check failed",
		"  Error: Authentication required. Please run 'agent login' first.",
		"  Fix the provider issue above, then try again.",
		"  No Humansh settings were changed.",
		"Installation stopped",
		"  Fix the provider issue above, then run the installer again.",
		"INSTALL_STATUS:0",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("logged-out Cursor recovery missing %q:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"Cursor CLI needs sign-in", "  Run        `agent login`", "Live check failed", "Executable \"", "humansh provider test cursor", "humansh doctor --provider cursor", "Installation details", "Incomplete", "make: ***", "Error 22", "INSTALL_STATUS:22", "rolling back the binary installation"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("logged-out Cursor recovery included obsolete output %q:\n%s", unwanted, text)
		}
	}
	installed := filepath.Join(home, ".local", "bin", "humansh")
	if _, err := os.Stat(installed); !os.IsNotExist(err) {
		t.Fatalf("stopped installation left a new binary behind: %v\n%s", err, text)
	}
	for _, path := range []string{filepath.Join(home, ".zshrc"), filepath.Join(home, "config", "humansh", "config.toml")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("failed setup changed %s: %v\n%s", path, err, text)
		}
	}
}

func runSetupInPTY(t *testing.T, binary string, env []string, answer bool, expected string) string {
	t.Helper()
	answerValue := "0"
	if answer {
		answerValue = "1"
	}
	const script = `zmodload zsh/zpty || exit 90
zpty -b S "$HUMANSH_SETUP_BINARY" setup
seen=''
provider_answered=0
answered=0
for attempt in {1..1000}; do
  while zpty -r -t S chunk; do seen+=$chunk; done
  if [[ $seen == *'AI provider [1]:'* && $provider_answered -eq 0 ]]; then
    if [[ $HUMANSH_SETUP_ANSWER != 1 ]]; then
      print -ru2 -- "unexpected provider prompt: ${(V)seen}"
      zpty -d S
      exit 91
    fi
    zpty -w -n S $'\r'
    provider_answered=1
  fi
  if [[ $seen == *'Continue? [Y/n]:'* && $answered -eq 0 ]]; then
    if [[ $HUMANSH_SETUP_ANSWER != 1 ]]; then
      print -ru2 -- "unexpected setup prompt: ${(V)seen}"
      zpty -d S
      exit 91
    fi
    zpty -w -n S $'\r'
    answered=1
  fi
  if [[ $seen == *"$HUMANSH_SETUP_EXPECTED"* ]]; then
    while zpty -r -t S chunk; do seen+=$chunk; done
    print -r -- "$seen"
    zpty -d S
    exit 0
  fi
  sleep 0.01
done
print -ru2 -- "setup did not finish: ${(V)seen}"
zpty -d S
exit 92`

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "zsh", "-f", "-c", script)
	command.Env = setEnvironment(env,
		"HUMANSH_SETUP_BINARY", binary,
		"HUMANSH_SETUP_ANSWER", answerValue,
		"HUMANSH_SETUP_EXPECTED", expected,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			t.Fatalf("setup PTY exceeded its timeout:\n%s", output)
		}
		t.Fatalf("setup PTY failed: %v\n%s", err, output)
	}
	return string(output)
}

func setupProbeCount(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatal(err)
	}
	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

func nonemptySetupLines(text string) int {
	count := 0
	for _, line := range strings.FieldsFunc(text, func(r rune) bool { return r == '\n' || r == '\r' }) {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}
