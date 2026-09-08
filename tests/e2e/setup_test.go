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

// TestMakeInstallUsesInvokingShell is the end-to-end regression for the
// shell-specific failures reported against `make install`: a fresh Bash install
// used to trust an inherited $SHELL naming Zsh; a later Zsh install used to stop
// at the recorded Bash integration; and Bash added after Zsh incorrectly showed
// Zsh's Enter onboarding instead of Bash's Ctrl-G shortcut.
func TestMakeInstallUsesInvokingShell(t *testing.T) {
	if os.Getenv("HUMANSH_RUN_E2E") != "1" {
		t.Skip("set HUMANSH_RUN_E2E=1 to run the installed-flow tests")
	}
	commands := make(map[string]string)
	for _, name := range []string{"go", "make", "zsh"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Fatalf("required E2E command %q is unavailable: %v", name, err)
		}
		commands[name] = path
	}
	if info, err := os.Stat("/bin/bash"); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("required E2E shell /bin/bash is unavailable: info=%v err=%v", info, err)
	}

	repo := repositoryRoot(t)
	root := t.TempDir()
	home := filepath.Join(root, "home")
	fixtureBin := filepath.Join(root, "bin")
	for _, directory := range []string{home, fixtureBin} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	fakeCodex := filepath.Join(fixtureBin, "codex")
	build := exec.Command("go", "build", "-trimpath", "-o", fakeCodex, "./tests/e2e/testdata/fakecodex")
	build.Dir = repo
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build deterministic Codex fixture: %v\n%s", err, output)
	}
	fakeBash := "#!/bin/sh\nprintf '%s\\n' 'GNU bash, version 5.3.15(1)-release'\n"
	if err := os.WriteFile(filepath.Join(fixtureBin, "bash"), []byte(fakeBash), 0o700); err != nil {
		t.Fatal(err)
	}

	wrapper := filepath.Join(root, "run-installer-from-bash")
	wrapperScript := "#!/bin/bash\n" + commands["make"] + " install\nstatus=$?\nprintf 'INSTALL_STATUS:%s\\n' \"$status\"\nexit 0\n"
	if err := os.WriteFile(wrapper, []byte(wrapperScript), 0o700); err != nil {
		t.Fatal(err)
	}
	restrictedPath := strings.Join([]string{
		fixtureBin,
		filepath.Dir(commands["go"]),
		filepath.Dir(commands["make"]),
		"/usr/bin",
		"/bin",
	}, string(os.PathListSeparator))
	env := setEnvironment(isolatedEnvironment(t, home, fixtureBin),
		"HUMANSH_NONINTERACTIVE", "0",
		"SHELL", "/bin/zsh",
		"NO_COLOR", "1",
		"TERM", "dumb",
		"PATH", restrictedPath,
	)

	const script = `zmodload zsh/zpty || exit 90
zpty -b I "$HUMANSH_E2E_WRAPPER"
seen=''
confirmed=0
for attempt in {1..2000}; do
  while zpty -r -t I chunk; do
    seen+=$chunk
    print -rn -- "$chunk"
  done
  if [[ $seen == *'Continue? [Y/n]:'* && $confirmed -eq 0 ]]; then
    zpty -w -n I $'\r'
    confirmed=1
  fi
  if [[ $seen == *'INSTALL_STATUS:'* ]]; then
    while zpty -r -t I chunk; do
      seen+=$chunk
      print -rn -- "$chunk"
    done
    zpty -d I
    exit 0
  fi
  sleep 0.01
done
print -ru2 -- "installer did not finish: ${(V)seen}"
zpty -d I
exit 91`
	runInstaller := func(wrapperPath string, runEnv []string) string {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, commands["zsh"], "-f", "-c", script)
		command.Dir = repo
		command.Env = setEnvironment(runEnv, "HUMANSH_E2E_WRAPPER", wrapperPath)
		output, err := command.CombinedOutput()
		if err != nil {
			if ctx.Err() != nil {
				t.Fatalf("installer exceeded its timeout:\n%s", output)
			}
			t.Fatalf("installer failed in its PTY wrapper: %v\n%s", err, output)
		}
		return string(output)
	}

	text := runInstaller(wrapper, env)
	for _, want := range []string{
		"  Shell      Bash",
		"  Startup    Update ~/.bashrc",
		"  Try it: open a new terminal, type `list files`, press Ctrl-G to translate, then Enter to run.",
		"INSTALL_STATUS:0",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("Bash install output missing %q:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"  Shell      Zsh", "~/.zshrc", "press Enter to translate"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("Bash install output included Zsh-specific information %q:\n%s", unwanted, text)
		}
	}
	bashrc := filepath.Join(home, ".bashrc")
	startup, err := os.ReadFile(bashrc)
	if err != nil || !strings.Contains(string(startup), "/shell/bash/humansh.bash") {
		t.Fatalf("Bash install did not activate ~/.bashrc: err=%v\n%s\n%s", err, startup, text)
	}
	bashAssetPath := filepath.Join(home, "data", "humansh", "shell", "bash", "humansh.bash")
	bashAsset, err := os.ReadFile(bashAssetPath)
	if err != nil {
		t.Fatalf("Bash install did not create its integration asset: %v\n%s", err, text)
	}
	for _, path := range []string{
		filepath.Join(home, ".zshrc"),
		filepath.Join(home, "data", "humansh", "shell", "zsh", "humansh.zsh"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("Bash-only install created Zsh path %s: %v\n%s", path, err, text)
		}
	}

	zshWrapper := filepath.Join(root, "run-installer-from-zsh")
	zshWrapperScript := "#!" + commands["zsh"] + "\n" + commands["make"] + " install\ninstall_status=$?\nprintf 'INSTALL_STATUS:%s\\n' \"$install_status\"\nexit 0\n"
	if err := os.WriteFile(zshWrapper, []byte(zshWrapperScript), 0o700); err != nil {
		t.Fatal(err)
	}
	zshText := runInstaller(zshWrapper, setEnvironment(env, "SHELL", "/bin/bash"))
	for _, want := range []string{
		"Review",
		"  Shells     Zsh and Bash",
		"  Provider   Codex",
		"  Startup    Update ~/.zshrc",
		"  Try it: open a new terminal, type `list files`, press Enter to translate, then Enter to run.",
		"INSTALL_STATUS:0",
	} {
		if !strings.Contains(zshText, want) {
			t.Errorf("Zsh reinstall output missing %q:\n%s", want, zshText)
		}
	}
	for _, unwanted := range []string{"Existing setup kept", "Remove Humansh from ~/.bashrc", "Refresh ~/.bashrc", "press Ctrl-G to translate"} {
		if strings.Contains(zshText, unwanted) {
			t.Errorf("Zsh install unexpectedly changed the existing Bash integration %q:\n%s", unwanted, zshText)
		}
	}
	bashStartup, err := os.ReadFile(bashrc)
	if err != nil || string(bashStartup) != string(startup) {
		t.Fatalf("Zsh install changed Bash activation: err=%v\nbefore=%s\nafter=%s\n%s", err, startup, bashStartup, zshText)
	}
	zshrc := filepath.Join(home, ".zshrc")
	zshStartup, err := os.ReadFile(zshrc)
	if err != nil || !strings.Contains(string(zshStartup), "/shell/zsh/humansh.zsh") {
		t.Fatalf("Zsh reinstall did not activate ~/.zshrc: err=%v\n%s\n%s", err, zshStartup, zshText)
	}
	if _, err := os.Stat(filepath.Join(home, "data", "humansh", "shell", "zsh", "humansh.zsh")); err != nil {
		t.Fatalf("Zsh install did not create its integration asset: %v\n%s", err, zshText)
	}
	bashAssetAfter, err := os.ReadFile(bashAssetPath)
	if err != nil || string(bashAssetAfter) != string(bashAsset) {
		t.Fatalf("Zsh install changed the Bash integration asset: err=%v\n%s", err, zshText)
	}

	reverseHome := filepath.Join(root, "reverse-home")
	if err := os.MkdirAll(reverseHome, 0o700); err != nil {
		t.Fatal(err)
	}
	reverseEnv := setEnvironment(isolatedEnvironment(t, reverseHome, fixtureBin),
		"HUMANSH_NONINTERACTIVE", "0",
		"SHELL", "/bin/bash",
		"NO_COLOR", "1",
		"TERM", "dumb",
		"PATH", restrictedPath,
	)
	initialZshText := runInstaller(zshWrapper, reverseEnv)
	for _, want := range []string{"  Shell      Zsh", "  Startup    Update ~/.zshrc", "press Enter to translate", "INSTALL_STATUS:0"} {
		if !strings.Contains(initialZshText, want) {
			t.Fatalf("initial Zsh install output missing %q:\n%s", want, initialZshText)
		}
	}
	reverseZshrc := filepath.Join(reverseHome, ".zshrc")
	reverseZshStartup, err := os.ReadFile(reverseZshrc)
	if err != nil {
		t.Fatalf("initial Zsh install did not activate ~/.zshrc: %v\n%s", err, initialZshText)
	}
	reverseZshAssetPath := filepath.Join(reverseHome, "data", "humansh", "shell", "zsh", "humansh.zsh")
	reverseZshAsset, err := os.ReadFile(reverseZshAssetPath)
	if err != nil {
		t.Fatalf("initial Zsh install did not create its integration asset: %v\n%s", err, initialZshText)
	}

	bashAfterZshText := runInstaller(wrapper, setEnvironment(reverseEnv, "SHELL", "/bin/zsh"))
	for _, want := range []string{
		"Review",
		"  Shells     Zsh and Bash",
		"  Startup    Update ~/.bashrc",
		"  Try it: open a new terminal, type `list files`, press Ctrl-G to translate, then Enter to run.",
		"INSTALL_STATUS:0",
	} {
		if !strings.Contains(bashAfterZshText, want) {
			t.Errorf("Bash-after-Zsh install output missing %q:\n%s", want, bashAfterZshText)
		}
	}
	for _, unwanted := range []string{"Refresh ~/.zshrc", "Remove Humansh from ~/.zshrc", "press Enter to translate"} {
		if strings.Contains(bashAfterZshText, unwanted) {
			t.Errorf("Bash install used Zsh-specific onboarding or changed Zsh %q:\n%s", unwanted, bashAfterZshText)
		}
	}
	reverseZshStartupAfter, err := os.ReadFile(reverseZshrc)
	if err != nil || string(reverseZshStartupAfter) != string(reverseZshStartup) {
		t.Fatalf("Bash install changed Zsh activation: err=%v\nbefore=%s\nafter=%s\n%s", err, reverseZshStartup, reverseZshStartupAfter, bashAfterZshText)
	}
	reverseZshAssetAfter, err := os.ReadFile(reverseZshAssetPath)
	if err != nil || string(reverseZshAssetAfter) != string(reverseZshAsset) {
		t.Fatalf("Bash install changed the Zsh integration asset: err=%v\n%s", err, bashAfterZshText)
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
