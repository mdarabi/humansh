package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/agenticlab-ai/humansh/internal/app"
	"github.com/agenticlab-ai/humansh/internal/bootstrap"
	"github.com/agenticlab-ai/humansh/internal/classifier"
	"github.com/agenticlab-ai/humansh/internal/commandgrammar"
	"github.com/agenticlab-ai/humansh/internal/config"
	usererr "github.com/agenticlab-ai/humansh/internal/errors"
	"github.com/agenticlab-ai/humansh/internal/llm"
	"github.com/agenticlab-ai/humansh/internal/shell"
	"github.com/agenticlab-ai/humansh/internal/shell/protocol"
)

func isolatedEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("OPENROUTER_API_KEY", "test-only")
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex"))
	t.Setenv("SHELL", "/bin/zsh")
	return home
}

func installReadyCodexFixture(t *testing.T, home string) {
	t.Helper()
	_ = home
	binDir := t.TempDir()
	script := `#!/bin/sh
case "${1-} ${2-}" in
  "exec Reply with exactly HUMANSH_READY and nothing else. Do not use tools or inspect external state.") echo "HUMANSH_READY" ;;
  *) exit 2 ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "codex"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func installBashVersionFixture(t *testing.T, version string) {
	t.Helper()
	binDir := t.TempDir()
	script := "#!/bin/sh\nprintf '%s\\n' 'GNU bash, version " + version + " (test)'\n"
	if err := os.WriteFile(filepath.Join(binDir, "bash"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func hideProvidersFromPath(t *testing.T) {
	t.Helper()
	zshPath, err := exec.LookPath("zsh")
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	if err := os.Symlink(zshPath, filepath.Join(binDir, "zsh")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
}

func TestClassifyJSONIncludesHintAndRejectsPositionals(t *testing.T) {
	isolatedEnv(t)
	var out, errOut bytes.Buffer
	code := Run(context.Background(), []string{"classify", "--json", "--first-token-kind", "alias"}, IO{In: strings.NewReader("gst"), Out: &out, Err: &errOut})
	if code != 0 || !strings.Contains(out.String(), `"first_token_kind": "alias"`) || strings.Contains(out.String(), `"raw"`) {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	out.Reset()
	errOut.Reset()
	code = Run(context.Background(), []string{"classify", "git status"}, IO{In: strings.NewReader(""), Out: &out, Err: &errOut})
	if code != 2 {
		t.Fatalf("positional input accepted: code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	out.Reset()
	errOut.Reset()
	code = Run(context.Background(), []string{"classify", "--first-token-kind", "command", "--resolved-command-path", "relative/tool"}, IO{In: strings.NewReader("tool status"), Out: &out, Err: &errOut})
	if code != 2 || !strings.Contains(errOut.String(), "invalid --resolved-command-path") {
		t.Fatalf("relative resolved path accepted: code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	out.Reset()
	errOut.Reset()
	code = Run(context.Background(), []string{"classify", "--first-token-kind", "builtin", "--resolved-command-path", "/bin/echo"}, IO{In: strings.NewReader("echo value"), Out: &out, Err: &errOut})
	if code != 2 || !strings.Contains(errOut.String(), "invalid --resolved-command-path") {
		t.Fatalf("resolved path accepted for non-command token: code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	out.Reset()
	errOut.Reset()
	code = Run(context.Background(), []string{"translate", "--first-token-kind", "command", "--resolved-command-path", "/bin/echo"}, IO{In: strings.NewReader("echo value"), Out: &out, Err: &errOut})
	if code != 2 || !strings.Contains(errOut.String(), "only valid for smart") {
		t.Fatalf("forced translation accepted unused resolved path: code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
}

func TestMachineFlagsRejectValuesOutsideFixedEnums(t *testing.T) {
	isolatedEnv(t)
	for _, args := range [][]string{
		{"smart", "--shell", "fish"},
		{"smart", "--first-token-kind", "executable"},
		{"classify", "--shell", "fish"},
		{"classify", "--first-token-kind", "maybe"},
		{"analyze", "--shell", "fish"},
	} {
		var out, errOut bytes.Buffer
		code := Run(context.Background(), args, IO{In: strings.NewReader("git status"), Out: &out, Err: &errOut})
		if code != 2 || errOut.Len() == 0 {
			t.Errorf("args=%v code=%d out=%q err=%q", args, code, out.String(), errOut.String())
		}
	}
}

func TestMachineCommandsAcceptBashReadlineProtocol(t *testing.T) {
	isolatedEnv(t)
	for _, test := range []struct {
		args []string
		code int
	}{
		{[]string{"classify", "--shell", "bash"}, 0},
		{[]string{"analyze", "--shell", "bash", "--protocol", protocol.ReadlineVersion}, protocol.ExitGeneratedLow},
	} {
		var out, errOut bytes.Buffer
		code := Run(context.Background(), test.args, IO{In: strings.NewReader("git status"), Out: &out, Err: &errOut})
		if code != test.code || errOut.Len() != 0 {
			t.Errorf("args=%v code=%d want=%d out=%q err=%q", test.args, code, test.code, out.String(), errOut.String())
		}
	}
}

func TestClassifyZLEStatusHintIsLocalAndFixed(t *testing.T) {
	isolatedEnv(t)
	for _, test := range []struct {
		input string
		kind  string
		want  string
	}{
		// The translate hint carries the provider label so the widget can show
		// "Translating with <provider>…" without spawning humansh at shell startup.
		{"show me files", "unresolved", "translate Codex"},
		{"list files", "unresolved", "translate Codex"},
		{"list files", "command", "literal"},
		{"pwd", "builtin", "literal"},
		{"gti status", "unresolved", "translate Codex"},
	} {
		var out, errOut bytes.Buffer
		code := Run(context.Background(), []string{"classify", "--zle-status", "--first-token-kind", test.kind}, IO{In: strings.NewReader(test.input), Out: &out, Err: &errOut})
		if code != 0 || out.String() != test.want || errOut.Len() != 0 {
			t.Errorf("input=%q code=%d out=%q err=%q", test.input, code, out.String(), errOut.String())
		}
	}
}

func TestAnalyzeHumanAndJSON(t *testing.T) {
	isolatedEnv(t)
	var out, errOut bytes.Buffer
	code := Run(context.Background(), []string{"analyze"}, IO{In: strings.NewReader("mv old new"), Out: &out, Err: &errOut})
	if code != protocol.ExitGeneratedMedium || !strings.Contains(out.String(), "Syntax: valid") || !strings.Contains(out.String(), "Risk: medium") {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	out.Reset()
	errOut.Reset()
	code = Run(context.Background(), []string{"analyze", "--json"}, IO{In: strings.NewReader("ls"), Out: &out, Err: &errOut})
	if code != protocol.ExitGeneratedLow || !strings.Contains(out.String(), `"syntax_valid": true`) || !strings.Contains(out.String(), `"level": "low"`) {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
}

func TestSetupNoShellChangeAndProviderUseValidation(t *testing.T) {
	home := isolatedEnv(t)
	hideProvidersFromPath(t)
	var out, errOut bytes.Buffer
	code := Run(context.Background(), []string{"setup", "--yes", "--no-shell-change"}, IO{In: strings.NewReader(""), Out: &out, Err: &errOut})
	if code != protocol.ExitProviderUnavailable || !strings.Contains(out.String(), "one ready AI provider is required") || !strings.Contains(out.String(), "No credential, configuration, or shell file was changed") || strings.Contains(out.String(), "1/6  Shell compatibility") || strings.Contains(out.String(), "Translation preferences") || strings.Contains(out.String(), "\x1b[") {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".zshrc")); !os.IsNotExist(err) {
		t.Fatalf("no-shell-change edited .zshrc: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "config", "humansh", "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("provider failure wrote config: %v", err)
	}

	out.Reset()
	errOut.Reset()
	code = Run(context.Background(), []string{"provider", "use", "openrouter"}, IO{In: strings.NewReader(""), Out: &out, Err: &errOut})
	if code != protocol.ExitProviderUnavailable || !strings.Contains(errOut.String(), "provider configure openrouter") {
		t.Fatalf("unconfigured provider accepted: code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	paths, err := config.ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	store := config.FileStore{Paths: paths}
	cfg := config.Default()
	if cfg.Provider != llm.Codex {
		t.Fatalf("failed provider switch changed config to %s", cfg.Provider)
	}
	cfg.OpenRouter.Model = "test/model"
	if err := store.SaveAtomic(cfg); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	code = Run(context.Background(), []string{"provider", "use", "openrouter"}, IO{In: strings.NewReader(""), Out: &out, Err: &errOut})
	if code != protocol.ExitProviderUnavailable || !strings.Contains(errOut.String(), "schema-proven") {
		t.Fatalf("unproven model bypassed configure probe: code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
}

func TestSetupShowsStartupPatchBeforeApplying(t *testing.T) {
	home := isolatedEnv(t)
	installReadyCodexFixture(t, home)
	startup := filepath.Join(home, ".zshrc")
	if err := os.WriteFile(startup, []byte("export PRIVATE_TOKEN=do-not-print\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := Run(context.Background(), []string{"setup", "--advanced", "--yes"}, IO{In: strings.NewReader(""), Out: &out, Err: &errOut})
	if code != 0 {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	for _, want := range []string{"5/6  Review", "Shell activation patch", "--- ~/.zshrc (before)", "+++ ~/.zshrc (after)", "+# >>> humansh >>>", "+export HUMANSH_FORCE_TRANSLATE_BINDING=", "+source ", "6/6  Complete"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("setup review missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "PRIVATE_TOKEN") || strings.Contains(out.String(), "do-not-print") {
		t.Fatalf("setup exposed unrelated .zshrc content:\n%s", out.String())
	}
}

func TestSetupExplainsStartupAccessFailure(t *testing.T) {
	home := isolatedEnv(t)
	installReadyCodexFixture(t, home)
	startup := filepath.Join(home, ".zshrc")
	if err := os.WriteFile(startup, []byte("keep\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := Run(context.Background(), []string{"setup", "--yes"}, IO{In: strings.NewReader(""), Out: &out, Err: &errOut})
	if code != protocol.ExitConfig || !strings.Contains(errOut.String(), "not owner-writable") || !strings.Contains(errOut.String(), "humansh setup --no-shell-change") || !strings.Contains(errOut.String(), "exact block") {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	if _, err := os.Stat(filepath.Join(home, "config", "humansh", "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("access failure wrote config: %v", err)
	}
}

func TestSetupFailsActionablyWhenZshIsUnavailable(t *testing.T) {
	home := isolatedEnv(t)
	t.Setenv("PATH", t.TempDir())
	var out, errOut bytes.Buffer
	code := Run(context.Background(), []string{"setup", "--yes"}, IO{In: strings.NewReader(""), Out: &out, Err: &errOut})
	if code != protocol.ExitConfig || !strings.Contains(errOut.String(), "install Zsh") || !strings.Contains(errOut.String(), "Nothing was changed or executed") {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".zshrc")); !os.IsNotExist(err) {
		t.Fatalf("failed setup changed startup file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "config", "humansh", "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("failed setup wrote config: %v", err)
	}
}

func TestSetupConfiguresBashReadlineIntegration(t *testing.T) {
	home := isolatedEnv(t)
	installReadyCodexFixture(t, home)
	installBashVersionFixture(t, "5.2.0")
	var out, errOut bytes.Buffer
	code := Run(context.Background(), []string{"setup", "--yes", "--shell", "bash"}, IO{In: strings.NewReader(""), Out: &out, Err: &errOut})
	if code != 0 {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	paths, err := config.ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := (config.FileStore{Paths: paths}).Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Shell.Name != shell.Bash || cfg.Shell.Protocol != protocol.ReadlineVersion || cfg.Shell.SmartEnter {
		t.Fatalf("bash configuration=%+v", cfg.Shell)
	}
	startup, err := os.ReadFile(filepath.Join(home, ".bashrc"))
	if err != nil || !strings.Contains(string(startup), "/shell/bash/humansh.bash") {
		t.Fatalf("bash startup=%q err=%v", startup, err)
	}
	if _, err := os.Stat(filepath.Join(paths.BashShellDir, "humansh.bash")); err != nil {
		t.Fatalf("Bash asset missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".zshrc")); !os.IsNotExist(err) {
		t.Fatalf("Bash setup unexpectedly edited .zshrc: %v", err)
	}
	for _, want := range []string{"Review", "Shell      Bash", "Provider   Codex", "Humansh is ready", "Try it: open a new terminal, type `list files`, press Ctrl-G to translate, then Enter to run."} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("Bash setup output missing %q:\n%s", want, out.String())
		}
	}
}

func TestSetupAutoConfiguresEveryAvailableShell(t *testing.T) {
	home := isolatedEnv(t)
	installReadyCodexFixture(t, home)
	installBashVersionFixture(t, "5.2.0")
	var out, errOut bytes.Buffer
	code := Run(context.Background(), []string{"setup", "--advanced", "--yes"}, IO{In: strings.NewReader(""), Out: &out, Err: &errOut})
	if code != 0 {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	paths, err := config.ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	state, err := config.LoadInstallState(paths.InstallState)
	if err != nil {
		t.Fatal(err)
	}
	if state.Version != 2 || !reflect.DeepEqual(state.ShellIDs(), []shell.ID{shell.Zsh, shell.Bash}) {
		t.Fatalf("multi-shell install state=%+v", state)
	}
	for _, check := range []struct {
		startup, asset, source, smart string
	}{
		{filepath.Join(home, ".zshrc"), filepath.Join(paths.ShellDir, "humansh.zsh"), "/shell/zsh/humansh.zsh", "HUMANSH_SMART_ENTER='1'"},
		{filepath.Join(home, ".bashrc"), filepath.Join(paths.BashShellDir, "humansh.bash"), "/shell/bash/humansh.bash", "HUMANSH_SMART_ENTER='0'"},
	} {
		startup, readErr := os.ReadFile(check.startup)
		if readErr != nil || !strings.Contains(string(startup), check.source) || !strings.Contains(string(startup), check.smart) {
			t.Errorf("startup %s=%q err=%v", check.startup, startup, readErr)
		}
		if _, statErr := os.Stat(check.asset); statErr != nil {
			t.Errorf("asset %s: %v", check.asset, statErr)
		}
	}
	for _, want := range []string{"Shell compatibility", "Bash 5.2.0", "compatible", "Shell integrations", "Zsh and Bash", "load automatically in each configured shell"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("automatic setup output missing %q:\n%s", want, out.String())
		}
	}
	for _, hidden := range []string{"Shell activation lets Humansh read and update the text at your prompt", "Zsh activation", "Bash activation"} {
		if strings.Contains(out.String(), hidden) {
			t.Errorf("Shell compatibility exposed hidden activation detail %q:\n%s", hidden, out.String())
		}
	}
}

func TestQuickSetupConfiguresOnlyTheLoginShell(t *testing.T) {
	home := isolatedEnv(t)
	installReadyCodexFixture(t, home)
	installBashVersionFixture(t, "5.2.0")
	t.Setenv("SHELL", "/bin/zsh")
	var out, errOut bytes.Buffer
	code := Run(context.Background(), []string{"setup", "--yes"}, IO{In: strings.NewReader(""), Out: &out, Err: &errOut})
	if code != 0 {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	paths, err := config.ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	state, err := config.LoadInstallState(paths.InstallState)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(state.ShellIDs(), []shell.ID{shell.Zsh}) {
		t.Fatalf("quick setup shell integrations=%v", state.ShellIDs())
	}
	if _, err := os.Stat(filepath.Join(home, ".bashrc")); !os.IsNotExist(err) {
		t.Fatalf("quick setup unexpectedly changed .bashrc: %v", err)
	}
	text := out.String()
	for _, want := range []string{"humansh setup", "Review", "Shell      Zsh", "Provider   Codex", "Humansh is ready", "Try it: open a new terminal, type `list files`, press Enter to translate, then Enter to run."} {
		if !strings.Contains(text, want) {
			t.Errorf("quick setup missing %q:\n%s", want, text)
		}
	}
	for _, hidden := range []string{"1/6", "Translation preferences", "Shell controls", "Shell activation patch", "Apply this setup?", "Controls   ", "One small Codex request"} {
		if strings.Contains(text, hidden) {
			t.Errorf("quick setup exposed advanced output %q:\n%s", hidden, text)
		}
	}
}

func TestQuickSetupFallsBackToAnAvailableShellWhenLoginShellIsUnknown(t *testing.T) {
	home := isolatedEnv(t)
	installReadyCodexFixture(t, home)
	codexPath, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	installBashVersionFixture(t, "5.2.0")
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	for name, target := range map[string]string{"codex": codexPath, "bash": bashPath} {
		if err := os.Symlink(target, filepath.Join(binDir, name)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", binDir)
	t.Setenv("SHELL", "")

	var out, errOut bytes.Buffer
	code := Run(context.Background(), []string{"setup", "--yes"}, IO{In: strings.NewReader(""), Out: &out, Err: &errOut})
	if code != 0 {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	paths, err := config.ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	state, err := config.LoadInstallState(paths.InstallState)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(state.ShellIDs(), []shell.ID{shell.Bash}) {
		t.Fatalf("fallback shell integrations=%v", state.ShellIDs())
	}
	if !strings.Contains(out.String(), "Shell      Bash") {
		t.Fatalf("fallback shell was not shown:\n%s", out.String())
	}
}

func TestQuickSetupProviderMenuRestoresOpenRouterAsFourthOption(t *testing.T) {
	ready := llm.Diagnostic{Installed: true, Configured: true, Authenticated: true, Available: true, AuthMode: "provider_managed"}
	providers := llm.MapRegistry{
		llm.Codex:      &setupTestProvider{id: llm.Codex, diagnostic: ready},
		llm.Claude:     &setupTestProvider{id: llm.Claude, diagnostic: ready},
		llm.Cursor:     &setupTestProvider{id: llm.Cursor, diagnostic: ready},
		llm.OpenRouter: &setupTestProvider{id: llm.OpenRouter, diagnostic: llm.Diagnostic{Installed: true, AuthMode: "missing"}},
	}
	runtime := bootstrap.Runtime{Engine: app.Engine{Providers: providers}}
	var out, errOut bytes.Buffer
	ui := interactiveSetupUI("4\n", &out, &errOut)

	selected, code := chooseQuickSetupProvider(context.Background(), llm.Codex, "", false, runtime, ui)
	if code != 0 || selected != llm.OpenRouter || errOut.Len() != 0 {
		t.Fatalf("selected=%s code=%d out=%s err=%s", selected, code, out.String(), errOut.String())
	}
	text := out.String()
	for _, want := range []string{"1  Codex", "2  Claude Code", "3  Cursor CLI", "4  OpenRouter", "AI provider [1]:"} {
		if !strings.Contains(text, want) {
			t.Errorf("quick provider menu missing %q:\n%s", want, text)
		}
	}
}

func TestQuickSetupOpenRouterPromptsForMissingKeyAndStagesIt(t *testing.T) {
	isolatedEnv(t)
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("PATH", t.TempDir())
	paths, err := config.ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	setup := &recordingProviderSetup{}
	runtime := bootstrap.Runtime{Paths: paths, ProviderSetup: setup}
	cfg := config.Default()
	var out, errOut bytes.Buffer
	ui := interactiveSetupUI("sk-or-quick-test\nprovider/quick-model\n", &out, &errOut)

	probeComplete, pending, code := prepareQuickSetupOpenRouter(context.Background(), runtime, &cfg, ui)
	if code != 0 || !probeComplete || pending == nil || pending.key != "sk-or-quick-test" {
		t.Fatalf("probeComplete=%t pending=%+v code=%d out=%s err=%s", probeComplete, pending, code, out.String(), errOut.String())
	}
	if cfg.Provider != llm.OpenRouter || cfg.OpenRouter.Model != "provider/quick-model" || !cfg.OpenRouter.StructuredOutputProven {
		t.Fatalf("OpenRouter was not staged in quick setup: %+v", cfg)
	}
	for _, want := range []string{"OPENROUTER_API_KEY", "Paste OpenRouter API key (input hidden)", "OpenRouter is ready with provider/quick-model"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("quick OpenRouter setup missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), pending.key) {
		t.Fatalf("quick OpenRouter setup echoed the API key:\n%s", out.String())
	}
	if _, err := os.Stat(paths.Credentials); !os.IsNotExist(err) {
		t.Fatalf("quick setup persisted the key before confirmation: %v", err)
	}
}

func TestQuickSetupOpenRouterUsesEnvironmentKeyWithoutPromptingForIt(t *testing.T) {
	isolatedEnv(t)
	t.Setenv("OPENROUTER_API_KEY", "sk-or-environment-test")
	paths, err := config.ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	setup := &recordingProviderSetup{}
	runtime := bootstrap.Runtime{Paths: paths, ProviderSetup: setup}
	cfg := config.Default()
	var out, errOut bytes.Buffer
	ui := interactiveSetupUI("provider/environment-model\n", &out, &errOut)

	probeComplete, pending, code := prepareQuickSetupOpenRouter(context.Background(), runtime, &cfg, ui)
	if code != 0 || !probeComplete || pending == nil || pending.key != "" {
		t.Fatalf("probeComplete=%t pending=%+v code=%d out=%s err=%s", probeComplete, pending, code, out.String(), errOut.String())
	}
	if setup.validatedKey != "sk-or-environment-test" || setup.probedKey != "sk-or-environment-test" {
		t.Fatalf("quick setup did not use the environment key: %+v", setup)
	}
	if !strings.Contains(out.String(), "Using OPENROUTER_API_KEY from this shell") {
		t.Fatalf("environment key source was not shown:\n%s", out.String())
	}
	if strings.Contains(out.String(), "Paste OpenRouter API key (input hidden):") || strings.Contains(out.String(), "sk-or-environment-test") {
		t.Fatalf("environment-backed setup prompted for or exposed the key:\n%s", out.String())
	}
}

func TestSetupShellVersionLabelsAreConcise(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		id       shell.ID
		raw      string
		expected string
	}{
		{shell.Zsh, "zsh 5.9 (arm64-apple-darwin)", "Zsh 5.9"},
		{shell.Bash, "GNU bash, version 3.2.57(1)-release", "Bash 3.2.57"},
		{shell.Bash, "", "Bash (version unavailable)"},
	} {
		if actual := setupShellVersionLabel(test.id, test.raw); actual != test.expected {
			t.Errorf("setupShellVersionLabel(%q, %q)=%q want %q", test.id, test.raw, actual, test.expected)
		}
	}
}

func TestSetupSkipsUnsupportedSecondaryShell(t *testing.T) {
	home := isolatedEnv(t)
	installReadyCodexFixture(t, home)
	installBashVersionFixture(t, "3.2.57")
	var out, errOut bytes.Buffer
	code := Run(context.Background(), []string{"setup", "--advanced", "--yes"}, IO{In: strings.NewReader(""), Out: &out, Err: &errOut})
	if code != 0 {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "Bash 3.2.57") || !strings.Contains(out.String(), "minimum required: Bash 4.3") {
		t.Fatalf("unsupported Bash was not explained:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".zshrc")); err != nil {
		t.Fatalf("Zsh integration missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".bashrc")); !os.IsNotExist(err) {
		t.Fatalf("unsupported Bash startup was changed: %v", err)
	}
}

func TestSetupRejectsAppleBashThreeActionably(t *testing.T) {
	home := isolatedEnv(t)
	installBashVersionFixture(t, "3.2.57")
	var out, errOut bytes.Buffer
	code := Run(context.Background(), []string{"setup", "--yes", "--shell", "bash"}, IO{In: strings.NewReader(""), Out: &out, Err: &errOut})
	if code != protocol.ExitConfig || !strings.Contains(errOut.String(), "Bash 4.3 or newer") || !strings.Contains(errOut.String(), "humansh setup") {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	for _, path := range []string{filepath.Join(home, ".bashrc"), filepath.Join(home, "config", "humansh", "config.toml")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("failed Bash setup changed %s: %v", path, err)
		}
	}
}

func TestQuickSetupRepairPreservesTheConfiguredProviderWithoutAProbe(t *testing.T) {
	home := isolatedEnv(t)
	installReadyCodexFixture(t, home)
	var out, errOut bytes.Buffer
	code := Run(context.Background(), []string{"setup", "--yes"}, IO{In: strings.NewReader(""), Out: &out, Err: &errOut})
	if code != 0 {
		t.Fatalf("initial setup code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	hideProvidersFromPath(t)
	out.Reset()
	errOut.Reset()
	code = Run(context.Background(), []string{"setup", "--repair"}, IO{In: strings.NewReader(""), Out: &out, Err: &errOut})
	if code != 0 || !strings.Contains(out.String(), "Provider   Codex") || !strings.Contains(out.String(), "Humansh is ready") || strings.Contains(out.String(), "Checking Codex") {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".zshrc")); err != nil {
		t.Fatalf("repair removed the Zsh integration: %v", err)
	}
}

func TestSetupRepairRequiresExistingConfiguration(t *testing.T) {
	home := isolatedEnv(t)
	var out, errOut bytes.Buffer
	code := Run(context.Background(), []string{"setup", "--repair"}, IO{In: strings.NewReader(""), Out: &out, Err: &errOut})
	if code != protocol.ExitConfig || !strings.Contains(errOut.String(), "no existing configuration to repair") || !strings.Contains(errOut.String(), "humansh setup") {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	for _, path := range []string{filepath.Join(home, ".zshrc"), filepath.Join(home, "config", "humansh", "config.toml")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("repair without config changed %s: %v", path, err)
		}
	}
}

func TestClassifierValuesMustComeFromStdin(t *testing.T) {
	home := isolatedEnv(t)
	var out, errOut bytes.Buffer
	code := Run(context.Background(), []string{"classifier", "add-command", "deploy"}, IO{In: strings.NewReader(""), Out: &out, Err: &errOut})
	if code != 2 {
		t.Fatalf("positional override accepted: code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	if _, err := os.Stat(filepath.Join(home, "config", "humansh", "classifier.toml")); !os.IsNotExist(err) {
		t.Fatalf("rejected override created file: %v", err)
	}
}

func TestDoctorDiagnosesMalformedFilesInsteadOfFailingBootstrap(t *testing.T) {
	home := isolatedEnv(t)
	configDir := filepath.Join(home, "config", "humansh")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("version = nope\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "classifier.toml"), []byte("version = nope\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := Run(context.Background(), []string{"doctor", "--json"}, IO{In: strings.NewReader(""), Out: &out, Err: &errOut})
	if code != protocol.ExitConfig || !strings.Contains(out.String(), "configuration file is malformed") || !strings.Contains(out.String(), "classifier override file is malformed") {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
}

func TestVersionDoesNotDependOnConfiguration(t *testing.T) {
	home := isolatedEnv(t)
	configDir := filepath.Join(home, "config", "humansh")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("malformed"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := Run(context.Background(), []string{"version", "--json"}, IO{In: strings.NewReader(""), Out: &out, Err: &errOut})
	if code != 0 || errOut.Len() != 0 {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	for _, field := range []string{`"version"`, `"commit"`, `"build_date"`, `"go_version"`, `"protocol"`, `"protocols"`, protocol.ReadlineVersion} {
		if !strings.Contains(out.String(), field) {
			t.Errorf("version JSON missing %s: %s", field, out.String())
		}
	}
	if code := Run(context.Background(), []string{"version", "extra"}, IO{Out: &out, Err: &errOut}); code != 2 {
		t.Fatalf("version accepted extra argument: %d", code)
	}
}

func TestCobraCommandTreeHelpDoesNotLoadConfiguration(t *testing.T) {
	home := isolatedEnv(t)
	configDir := filepath.Join(home, "config", "humansh")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("malformed"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--help"}, {"smart", "--help"}, {"help", "doctor"}, {"onboarding", "--help"}, {"uninstall", "--help"}} {
		var out, errOut bytes.Buffer
		code := Run(context.Background(), args, IO{In: strings.NewReader(""), Out: &out, Err: &errOut})
		if code != 0 || errOut.Len() != 0 || !strings.Contains(out.String(), "Usage:") {
			t.Errorf("args=%v code=%d out=%s err=%s", args, code, out.String(), errOut.String())
		}
	}
}

func TestUninstallCommandRemovesInstallationWithoutLoadingConfig(t *testing.T) {
	home, paths := prepareCLIUninstall(t)
	if err := os.WriteFile(paths.ConfigFile, []byte("malformed = [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := Run(context.Background(), []string{"uninstall"}, IO{In: strings.NewReader(""), Out: &out, Err: &errOut})
	if code != 0 || !strings.Contains(out.String(), "configuration and credentials were preserved") || !strings.Contains(out.String(), "cannot alter the parent shell process") || !strings.Contains(out.String(), "restart that shell") || errOut.Len() != 0 {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	for _, removed := range []string{paths.Binary, paths.InstallState, filepath.Join(paths.ShellDir, "humansh.zsh")} {
		if _, err := os.Lstat(removed); !os.IsNotExist(err) {
			t.Errorf("uninstall retained %s: %v", removed, err)
		}
	}
	if _, err := os.Stat(paths.ConfigFile); err != nil {
		t.Fatalf("default uninstall removed config: %v", err)
	}
	startup, err := os.ReadFile(filepath.Join(home, ".zshrc"))
	if err != nil || strings.Contains(string(startup), "humansh") || !strings.Contains(string(startup), "keep-user-setting") {
		t.Fatalf("startup=%q err=%v", startup, err)
	}
}

func TestUninstallCommandUsesShellAgnosticReloadMessage(t *testing.T) {
	home := isolatedEnv(t)
	paths, err := config.ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte("keep-user-setting\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.Binary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Binary, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Shell.Name = shell.Bash
	cfg.Shell.Protocol = protocol.ReadlineVersion
	cfg.Shell.SmartEnter = false
	if err := (config.FileStore{Paths: paths}).SaveAtomic(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Setup(paths, cfg, "test"); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := Run(context.Background(), []string{"uninstall"}, IO{In: strings.NewReader(""), Out: &out, Err: &errOut})
	if code != 0 || !strings.Contains(out.String(), "parent shell process") || !strings.Contains(out.String(), "restart that shell") || !strings.Contains(out.String(), "in-memory bindings") || errOut.Len() != 0 {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	for _, removed := range []string{paths.Binary, paths.InstallState, filepath.Join(paths.BashShellDir, "humansh.bash")} {
		if _, err := os.Lstat(removed); !os.IsNotExist(err) {
			t.Errorf("Bash uninstall retained %s: %v", removed, err)
		}
	}
}

func TestUninstallCommandPurgeRequiresConfirmation(t *testing.T) {
	for _, answer := range []string{"n\n", "\n"} {
		_, paths := prepareCLIUninstall(t)
		var out, errOut bytes.Buffer
		code := Run(context.Background(), []string{"uninstall", "--purge"}, IO{In: strings.NewReader(answer), Out: &out, Err: &errOut})
		if code != 0 || !strings.Contains(out.String(), "Uninstall cancelled. Nothing was changed.") || strings.Contains(out.String(), "humansh uninstalled") || errOut.Len() != 0 {
			t.Fatalf("answer=%q code=%d out=%s err=%s", answer, code, out.String(), errOut.String())
		}
		for _, preserved := range []string{paths.Binary, paths.ConfigFile, paths.InstallState, filepath.Join(paths.ShellDir, "humansh.zsh")} {
			if _, err := os.Stat(preserved); err != nil {
				t.Fatalf("answer=%q declined purge changed %s: %v", answer, preserved, err)
			}
		}
	}

	_, paths := prepareCLIUninstall(t)
	installCLIFakeSecurity(t)
	var out, errOut bytes.Buffer
	out.Reset()
	errOut.Reset()
	code := Run(context.Background(), []string{"uninstall", "--purge", "--yes"}, IO{In: strings.NewReader(""), Out: &out, Err: &errOut})
	if code != 0 || !strings.Contains(out.String(), "were purged") || errOut.Len() != 0 {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	for _, removed := range []string{paths.Binary, paths.ConfigDir, paths.DataDir} {
		if _, err := os.Lstat(removed); !os.IsNotExist(err) {
			t.Errorf("confirmed purge retained %s: %v", removed, err)
		}
	}

	out.Reset()
	errOut.Reset()
	code = Run(context.Background(), []string{"uninstall", "--yes"}, IO{In: strings.NewReader(""), Out: &out, Err: &errOut})
	if code != 2 || !strings.Contains(errOut.String(), "valid only with --purge") {
		t.Fatalf("standalone --yes code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
}

func prepareCLIUninstall(t *testing.T) (string, config.Paths) {
	t.Helper()
	home := isolatedEnv(t)
	paths, err := config.ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("keep-user-setting\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.Binary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Binary, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	store := config.FileStore{Paths: paths}
	if err := store.SaveAtomic(config.Default()); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Setup(paths, config.Default(), "test"); err != nil {
		t.Fatal(err)
	}
	return home, paths
}

func installCLIFakeSecurity(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		return
	}
	fakeBin := t.TempDir()
	security := filepath.Join(fakeBin, "security")
	if err := os.WriteFile(security, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+"/usr/bin:/bin")
}

func TestClassifyAndLiteralProtocol(t *testing.T) {
	isolatedEnv(t)
	var out, errOut bytes.Buffer
	code := Run(context.Background(), []string{"classify", "--first-token-kind", "command"}, IO{In: strings.NewReader("find all files modified today"), Out: &out, Err: &errOut})
	if code != 0 || !strings.Contains(out.String(), "Classification: ambiguous") {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	out.Reset()
	errOut.Reset()
	code = Run(context.Background(), []string{"smart", "--protocol", "zle-v1", "--shell", "zsh", "--first-token-kind", "command"}, IO{In: strings.NewReader("fixture status"), Out: &out, Err: &errOut})
	if code != protocol.ExitLiteral || out.Len() != 0 || errOut.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestCommandGrammarIsSharedAcrossShellProtocols(t *testing.T) {
	isolatedEnv(t)
	resolvedPath := "/fixture/bin/git"
	runtime, err := bootstrap.Build()
	if err != nil {
		t.Fatal(err)
	}
	runtime.Engine.Classifier = classifier.Classifier{Invocations: commandgrammar.NewAnalyzer(sharedProtocolHelpSource{})}
	tests := []struct {
		shellID string
		version string
	}{
		{shellID: "zsh", version: protocol.Version},
		{shellID: "bash", version: protocol.ReadlineVersion},
	}
	var classifications []string
	for _, test := range tests {
		var out, errOut bytes.Buffer
		commonFlags := []string{"--shell", test.shellID, "--first-token-kind", "command", "--resolved-command-path", resolvedPath}
		smartArgs := append([]string{"smart", "--protocol", test.version}, commonFlags...)
		code := runProtocol(context.Background(), "smart", smartArgs[1:], runtime, IO{In: strings.NewReader("git is failing please authenticate"), Out: &out, Err: &errOut})
		if code != protocol.ExitAmbiguous || out.Len() != 0 || !strings.Contains(errOut.String(), "Not sure whether this is English or a command") {
			t.Fatalf("shell=%s code=%d stdout=%q stderr=%q", test.shellID, code, out.String(), errOut.String())
		}
		for _, literal := range []string{"git status", `git commit -m "please authenticate"`} {
			out.Reset()
			errOut.Reset()
			code = runProtocol(context.Background(), "smart", smartArgs[1:], runtime, IO{In: strings.NewReader(literal), Out: &out, Err: &errOut})
			if code != protocol.ExitLiteral || out.Len() != 0 || errOut.Len() != 0 {
				t.Fatalf("shell=%s input=%q code=%d stdout=%q stderr=%q", test.shellID, literal, code, out.String(), errOut.String())
			}
		}

		out.Reset()
		errOut.Reset()
		classifyArgs := append([]string{"classify", "--json"}, commonFlags...)
		code = runClassify(context.Background(), classifyArgs[1:], runtime, IO{In: strings.NewReader("git is failing please authenticate"), Out: &out, Err: &errOut})
		if code != 0 || errOut.Len() != 0 || !strings.Contains(out.String(), `"source": "installed_help"`) || !strings.Contains(out.String(), `"command_grammar_undocumented_subcommand"`) || strings.Contains(out.String(), resolvedPath) || strings.Contains(out.String(), "failing please authenticate") {
			t.Fatalf("shell=%s classify code=%d stdout=%q stderr=%q", test.shellID, code, out.String(), errOut.String())
		}
		classifications = append(classifications, out.String())
	}
	if len(classifications) != 2 || classifications[0] != classifications[1] {
		t.Fatalf("shell protocols produced different shared classifications:\nzsh=%s\nbash=%s", classifications[0], classifications[1])
	}
}

type sharedProtocolHelpSource struct{}

func (sharedProtocolHelpSource) Open(context.Context, commandgrammar.ExecutableRef) (commandgrammar.HelpSession, error) {
	return sharedProtocolHelpSession{}, nil
}

type sharedProtocolHelpSession struct{}

func (sharedProtocolHelpSession) Load(_ context.Context, prefix []string) commandgrammar.HelpResult {
	root := commandgrammar.NodeSpec{
		Options: map[string]commandgrammar.OptionSpec{"--no-pager": {}}, OptionsKnown: true,
		Subcommands: map[string]struct{}{"status": {}, "commit": {}}, SubcommandState: commandgrammar.SubcommandsListed,
		SubcommandsComplete: true, Complete: true,
	}
	status := commandgrammar.NodeSpec{
		Options: map[string]commandgrammar.OptionSpec{"--short": {}}, OptionsKnown: true,
		SubcommandState: commandgrammar.SubcommandsNone, Complete: true,
	}
	commit := commandgrammar.NodeSpec{
		Options: map[string]commandgrammar.OptionSpec{"-m": {Value: commandgrammar.RequiredValue, AllowSeparate: true}}, OptionsKnown: true,
		SubcommandState: commandgrammar.SubcommandsNone, Complete: true,
	}
	switch strings.Join(prefix, " ") {
	case "":
		return commandgrammar.HelpResult{Node: root, Status: commandgrammar.HelpOK}
	case "status":
		return commandgrammar.HelpResult{Node: status, Status: commandgrammar.HelpOK}
	case "commit":
		return commandgrammar.HelpResult{Node: commit, Status: commandgrammar.HelpOK}
	default:
		return commandgrammar.HelpResult{Status: commandgrammar.HelpUnavailable}
	}
}

func (sharedProtocolHelpSession) Close() error { return nil }

func TestSetupAndClassifierStdin(t *testing.T) {
	home := isolatedEnv(t)
	hideProvidersFromPath(t)
	var out, errOut bytes.Buffer
	code := Run(context.Background(), []string{"setup"}, IO{In: strings.NewReader(""), Out: &out, Err: &errOut})
	if code != protocol.ExitProviderUnavailable || !strings.Contains(out.String(), "one ready AI provider is required") {
		t.Fatalf("setup code=%d err=%s", code, errOut.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".zshrc")); !os.IsNotExist(err) {
		t.Fatalf("provider failure changed .zshrc: %v", err)
	}
	out.Reset()
	errOut.Reset()
	code = Run(context.Background(), []string{"classifier", "add-command"}, IO{In: strings.NewReader("deploy"), Out: &out, Err: &errOut})
	if code != 0 {
		t.Fatalf("classifier code=%d err=%s", code, errOut.String())
	}
	out.Reset()
	code = Run(context.Background(), []string{"classify", "--first-token-kind", "unresolved"}, IO{In: strings.NewReader("deploy production"), Out: &out, Err: &errOut})
	if code != 0 || !strings.Contains(out.String(), "Classification: literal") {
		t.Fatalf("out=%s err=%s", out.String(), errOut.String())
	}
}

type setupTestProvider struct {
	id              llm.ProviderID
	diagnostic      llm.Diagnostic
	probeDiagnostic *llm.Diagnostic
	probeFunc       func(context.Context) llm.Diagnostic
	probeCalls      int
}

type recordingProviderSetup struct {
	validatedModel, capabilityModel, probedModel string
	validatedKey, capabilityKey, probedKey       string
}

type retryingOpenRouterSetup struct {
	recordingProviderSetup
	validations int
}

type failingOpenRouterSetup struct {
	recordingProviderSetup
}

type retryingModelProviderSetup struct {
	*recordingProviderSetup
	modelChecks int
}

func (s *retryingModelProviderSetup) ValidateOpenRouterModel(ctx context.Context, cfg config.RuntimeConfig, model, key string) error {
	s.modelChecks++
	s.capabilityModel, s.capabilityKey = model, key
	if s.modelChecks == 1 {
		return usererr.WithExit(protocol.ExitProviderUnavailable, "openrouter_structured_output_unsupported", "OpenRouter model "+model+" does not support strict structured output.", "The free catalog check found the model, but its current endpoints do not advertise the structured_outputs capability humansh requires. No compatibility request was sent and no model credits were used.", false, nil)
	}
	return s.recordingProviderSetup.ValidateOpenRouterModel(ctx, cfg, model, key)
}

func (s *failingOpenRouterSetup) ProbeOpenRouter(_ context.Context, _ config.RuntimeConfig, model, key string) (llm.TranslationResponse, error) {
	s.probedModel, s.probedKey = model, key
	return llm.TranslationResponse{}, errors.New("model rejected the test schema")
}

func (s *retryingOpenRouterSetup) ValidateOpenRouterKey(ctx context.Context, cfg config.RuntimeConfig, model, key string) error {
	s.validations++
	if s.validations == 1 {
		return errors.New("invalid test key")
	}
	return s.recordingProviderSetup.ValidateOpenRouterKey(ctx, cfg, model, key)
}

func (s *recordingProviderSetup) ValidateOpenRouterKey(_ context.Context, _ config.RuntimeConfig, model, key string) error {
	s.validatedModel, s.validatedKey = model, key
	return nil
}

func (s *recordingProviderSetup) ValidateOpenRouterModel(_ context.Context, _ config.RuntimeConfig, model, key string) error {
	s.capabilityModel, s.capabilityKey = model, key
	return nil
}

func (s *recordingProviderSetup) ProbeOpenRouter(_ context.Context, _ config.RuntimeConfig, model, key string) (llm.TranslationResponse, error) {
	s.probedModel, s.probedKey = model, key
	return llm.TranslationResponse{Status: "ok", Command: "pwd", Explanation: "Prints the directory.", Assumptions: []string{}}, nil
}

func TestOpenRouterConfigurationDelegatesProbeAndPersistsExactProof(t *testing.T) {
	isolatedEnv(t)
	runtime, err := bootstrap.Build()
	if err != nil {
		t.Fatal(err)
	}
	setup := &recordingProviderSetup{}
	runtime.ProviderSetup = setup
	var out, errOut bytes.Buffer
	code := configureOpenRouter(context.Background(), []string{"--model", "provider/test-model"}, runtime, IO{In: strings.NewReader(""), Out: &out, Err: &errOut})
	if code != 0 {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	if setup.validatedModel != "provider/test-model" || setup.capabilityModel != "provider/test-model" || setup.probedModel != "provider/test-model" || setup.validatedKey != "test-only" || setup.capabilityKey != "test-only" || setup.probedKey != "test-only" {
		t.Fatalf("setup calls=%+v", setup)
	}
	loaded, err := runtime.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.OpenRouter.Model != "provider/test-model" || !loaded.OpenRouter.StructuredOutputProven || loaded.OpenRouter.StructuredOutputModel != "provider/test-model" {
		t.Fatalf("saved config=%+v", loaded.OpenRouter)
	}
}

func TestInteractiveSetupConfiguresOpenRouterInlineWithoutSavingBeforeReview(t *testing.T) {
	isolatedEnv(t)
	t.Setenv("OPENROUTER_API_KEY", "")
	// Prevent a developer's real macOS Keychain entry from affecting this test.
	t.Setenv("PATH", t.TempDir())
	paths, err := config.ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	setup := &recordingProviderSetup{}
	runtime := bootstrap.Runtime{Paths: paths, ProviderSetup: setup}
	cfg := config.Default()
	var out, errOut bytes.Buffer
	ui := interactiveSetupUI("sk-or-inline-test\nprovider/test-model\n", &out, &errOut)

	ready, pending, code := configureSetupOpenRouter(context.Background(), runtime, &cfg, ui)
	if code != 0 || !ready || pending == nil || pending.key != "sk-or-inline-test" {
		t.Fatalf("ready=%t pending=%+v code=%d out=%s err=%s", ready, pending, code, out.String(), errOut.String())
	}
	if setup.validatedModel != "provider/test-model" || setup.capabilityModel != "provider/test-model" || setup.probedModel != "provider/test-model" || setup.validatedKey != pending.key || setup.capabilityKey != pending.key || setup.probedKey != pending.key {
		t.Fatalf("setup calls=%+v", setup)
	}
	if cfg.Provider != llm.OpenRouter || cfg.OpenRouter.Model != "provider/test-model" || !cfg.OpenRouter.StructuredOutputProven || cfg.OpenRouter.StructuredOutputModel != cfg.OpenRouter.Model {
		t.Fatalf("configured OpenRouter=%+v provider=%s", cfg.OpenRouter, cfg.Provider)
	}
	if _, err := os.Stat(paths.Credentials); !os.IsNotExist(err) {
		t.Fatalf("inline setup persisted the key before final review: %v", err)
	}
	text := out.String()
	for _, want := range []string{"https://openrouter.ai/settings/keys", "https://openrouter.ai/models", "input hidden", "without using model credits", "small automatic compatibility request", "required minimal compatibility check", "saved after final confirmation"} {
		if !strings.Contains(text, want) {
			t.Errorf("OpenRouter setup guidance missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, pending.key) {
		t.Fatalf("inline setup echoed the API key:\n%s", text)
	}
	if strings.Contains(text, "Run the metered compatibility probe now?") {
		t.Fatalf("inline setup asked for approval for a required check:\n%s", text)
	}
}

func TestInlineOpenRouterSetupAcceptsAnotherModelDirectlyAfterCapabilityFailure(t *testing.T) {
	isolatedEnv(t)
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("PATH", t.TempDir())
	paths, err := config.ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	setup := &recordingProviderSetup{}
	retrying := &retryingModelProviderSetup{recordingProviderSetup: setup}
	runtime := bootstrap.Runtime{Paths: paths, ProviderSetup: retrying}
	cfg := config.Default()
	var out, errOut bytes.Buffer
	ui := interactiveSetupUI("sk-or-unsupported\nstealth/ox-alpha\nz-ai/glm-5.3\n", &out, &errOut)

	ready, pending, code := configureSetupOpenRouter(context.Background(), runtime, &cfg, ui)
	if code != 0 || !ready || pending == nil || pending.key != "sk-or-unsupported" {
		t.Fatalf("ready=%t pending=%+v code=%d out=%s err=%s", ready, pending, code, out.String(), errOut.String())
	}
	if retrying.modelChecks != 2 || setup.capabilityModel != "z-ai/glm-5.3" || setup.probedModel != "z-ai/glm-5.3" {
		t.Fatalf("model checks=%d setup calls=%+v", retrying.modelChecks, setup)
	}
	if setup.validatedModel != "stealth/ox-alpha" {
		t.Fatalf("API key was unexpectedly revalidated for the second model: %+v", setup)
	}
	if cfg.OpenRouter.Model != "z-ai/glm-5.3" || !cfg.OpenRouter.StructuredOutputProven {
		t.Fatalf("second model was not configured: %+v", cfg.OpenRouter)
	}
	for _, want := range []string{"does not support strict structured output", "No compatibility request was sent", "supported_parameters=structured_outputs", "paste another compatible provider/model ID", "OpenRouter is ready with z-ai/glm-5.3"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("model-retry guidance omitted %q:\n%s", want, out.String())
		}
	}
	for _, unwanted := range []string{"Please answer yes or no", "Try a different OpenRouter model?"} {
		if strings.Contains(out.String(), unwanted) {
			t.Errorf("model retry included obsolete prompt %q:\n%s", unwanted, out.String())
		}
	}
}

func TestChoosingOpenRouterFromSetupMenuConfiguresItInPlace(t *testing.T) {
	isolatedEnv(t)
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("PATH", t.TempDir())
	paths, err := config.ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	openRouter := &setupTestProvider{id: llm.OpenRouter, diagnostic: llm.Diagnostic{Installed: true, AuthMode: "missing", Message: "OpenRouter API key is not configured"}}
	setup := &recordingProviderSetup{}
	runtime := bootstrap.Runtime{
		Engine:        app.Engine{Providers: llm.MapRegistry{llm.OpenRouter: openRouter}},
		Config:        config.Default(),
		Paths:         paths,
		ProviderSetup: setup,
	}
	cfg := runtime.Config
	var pending *setupOpenRouterCredential
	var out, errOut bytes.Buffer
	ui := interactiveSetupUI("4\nsk-or-menu-test\nprovider/menu-model\n", &out, &errOut)

	selected, ready, code := configureSetupProvider(context.Background(), &runtime, &cfg, "", false, ui, &pending)
	if code != 0 || !ready || selected != llm.OpenRouter || pending == nil {
		t.Fatalf("selected=%s ready=%t pending=%+v code=%d out=%s err=%s", selected, ready, pending, code, out.String(), errOut.String())
	}
	text := out.String()
	if strings.Contains(text, "humansh provider configure openrouter") || strings.Contains(text, "Choose a different provider?") {
		t.Fatalf("setup sent the user to a second workflow:\n%s", text)
	}
	if !strings.Contains(text, "OpenRouter is ready with provider/menu-model") {
		t.Fatalf("setup did not finish OpenRouter inline:\n%s", text)
	}
}

func TestFailedOpenRouterProofReturnsToModelPromptWithoutSaving(t *testing.T) {
	isolatedEnv(t)
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("PATH", t.TempDir())
	paths, err := config.ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	runtime := bootstrap.Runtime{
		Paths:         paths,
		ProviderSetup: &failingOpenRouterSetup{},
	}
	cfg := config.Default()
	var out, errOut bytes.Buffer
	ui := interactiveSetupUI("sk-or-failed-proof\nprovider/model\nback\n", &out, &errOut)

	ready, pending, code := configureSetupOpenRouter(context.Background(), runtime, &cfg, ui)
	if code != setupChooseDifferentProvider || ready || pending != nil {
		t.Fatalf("ready=%t pending=%+v code=%d out=%s err=%s", ready, pending, code, out.String(), errOut.String())
	}
	if cfg.Provider != config.Default().Provider || cfg.OpenRouter.Model != "" || cfg.OpenRouter.StructuredOutputProven {
		t.Fatalf("failed provider proof changed config: %+v", cfg)
	}
	if _, err := os.Stat(paths.Credentials); !os.IsNotExist(err) {
		t.Fatalf("failed proof saved credentials: %v", err)
	}
	text := out.String()
	for _, want := range []string{"did not pass the compatibility probe", "paste another compatible provider/model ID", "OpenRouter model ID (provider/model)"} {
		if !strings.Contains(text, want) {
			t.Errorf("provider-proof failure missing %q:\n%s", want, text)
		}
	}
}

func TestInlineOpenRouterSetupCanCorrectARejectedNewKey(t *testing.T) {
	isolatedEnv(t)
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("PATH", t.TempDir())
	paths, err := config.ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	setup := &retryingOpenRouterSetup{}
	runtime := bootstrap.Runtime{Paths: paths, ProviderSetup: setup}
	cfg := config.Default()
	var out, errOut bytes.Buffer
	ui := interactiveSetupUI("sk-or-rejected\nprovider/model\n\nsk-or-corrected\nprovider/model\n", &out, &errOut)

	ready, pending, code := configureSetupOpenRouter(context.Background(), runtime, &cfg, ui)
	if code != 0 || !ready || pending == nil || pending.key != "sk-or-corrected" || setup.validations != 2 {
		t.Fatalf("ready=%t pending=%+v validations=%d code=%d out=%s err=%s", ready, pending, setup.validations, code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "Paste a different OpenRouter API key?") {
		t.Fatalf("setup did not offer an inline correction:\n%s", out.String())
	}
	if strings.Contains(out.String(), "sk-or-rejected") || strings.Contains(out.String(), "sk-or-corrected") {
		t.Fatalf("setup echoed a rejected or replacement key:\n%s", out.String())
	}
}

func TestSetupCancellationAcknowledgesAnOpenRouterCompatibilityCheck(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	printSetupCancellation(&out, true)
	for _, want := range []string{"OpenRouter compatibility check ran", "no credential, configuration, or shell file was changed"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("cancellation message missing %q: %s", want, out.String())
		}
	}
}

func (p *setupTestProvider) ID() llm.ProviderID { return p.id }
func (p *setupTestProvider) Diagnose(context.Context) llm.Diagnostic {
	return p.diagnostic
}
func (p *setupTestProvider) Probe(ctx context.Context) llm.Diagnostic {
	p.probeCalls++
	if p.probeFunc != nil {
		diagnostic := p.probeFunc(ctx)
		diagnostic.LiveCheck = true
		return diagnostic
	}
	diagnostic := p.diagnostic
	if p.probeDiagnostic != nil {
		diagnostic = *p.probeDiagnostic
	}
	diagnostic.LiveCheck = true
	return diagnostic
}
func (*setupTestProvider) Translate(context.Context, llm.TranslationRequest) (llm.TranslationResponse, error) {
	return llm.TranslationResponse{}, nil
}

func TestProviderHelpExplainsCommandsAndNextStep(t *testing.T) {
	t.Parallel()
	rt := bootstrap.Runtime{Config: config.Default()}
	rt.Config.Provider = llm.Cursor
	var out, errOut bytes.Buffer

	code := runProvider(context.Background(), nil, rt, IO{Out: &out, Err: &errOut})
	if code != 0 || errOut.Len() != 0 {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	for _, want := range []string{
		"Manage AI providers",
		"Current: Cursor CLI (cursor)",
		"list [--json]",
		"use <name>",
		"select <name>",
		"configure <name> [options]",
		"test [name]",
		"Provider names:",
		"humansh provider list",
		"Next:",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("provider help missing %q:\n%s", want, out.String())
		}
	}

	out.Reset()
	code = runProvider(context.Background(), []string{"configure"}, rt, IO{Out: &out, Err: &errOut})
	if code != 0 || !strings.Contains(out.String(), "Inspect CLI authentication ownership or configure an API provider") || !strings.Contains(out.String(), "configure openrouter --model") {
		t.Fatalf("configure help code=%d out=%s err=%s", code, out.String(), errOut.String())
	}

	out.Reset()
	code = runProvider(context.Background(), []string{"select", "--help"}, rt, IO{Out: &out, Err: &errOut})
	if code != 0 || !strings.Contains(out.String(), "Verify and select the active provider") {
		t.Fatalf("select help code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
}

func TestProviderCobraHelpUsesOrientedGuide(t *testing.T) {
	t.Parallel()
	var out, errOut bytes.Buffer
	code := Run(context.Background(), []string{"provider", "--help"}, IO{Out: &out, Err: &errOut})
	if code != 0 || errOut.Len() != 0 {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "Manage AI providers") || !strings.Contains(out.String(), "humansh provider configure openrouter") {
		t.Fatalf("provider --help was not oriented:\n%s", out.String())
	}

	out.Reset()
	code = Run(context.Background(), []string{"provider", "configure", "--help"}, IO{Out: &out, Err: &errOut})
	if code != 0 || errOut.Len() != 0 || !strings.Contains(out.String(), "Inspect CLI authentication ownership or configure an API provider") {
		t.Fatalf("provider configure --help code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
}

func TestProviderListIsReadableAndMarksCurrentProvider(t *testing.T) {
	t.Parallel()
	providers := llm.MapRegistry{
		llm.OpenRouter: &setupTestProvider{id: llm.OpenRouter, diagnostic: llm.Diagnostic{
			Installed: true, AuthMode: "missing", Message: "API key not configured",
		}},
		llm.Cursor: &setupTestProvider{id: llm.Cursor, diagnostic: llm.Diagnostic{
			Installed: true, Configured: true, AuthMode: "provider_managed",
			NextSteps: []llm.DiagnosticAction{{Description: "Run a live provider check", Command: "humansh provider test cursor"}},
		}},
		llm.Claude: &setupTestProvider{id: llm.Claude, diagnostic: llm.Diagnostic{
			Installed: true, Configured: true, AuthMode: "provider_managed",
			NextSteps: []llm.DiagnosticAction{{Description: "Run a live provider check", Command: "humansh provider test claude"}},
		}},
		llm.Codex: &setupTestProvider{id: llm.Codex, diagnostic: llm.Diagnostic{
			Installed: true, Configured: true, AuthMode: "provider_managed",
			NextSteps: []llm.DiagnosticAction{{Description: "Run a live provider check", Command: "humansh provider test codex"}},
		}},
	}
	cfg := config.Default()
	cfg.Provider = llm.Cursor
	rt := bootstrap.Runtime{Engine: app.Engine{Providers: providers}, Config: cfg}
	var out, errOut bytes.Buffer

	code := runProvider(context.Background(), []string{"list"}, rt, IO{Out: &out, Err: &errOut})
	if code != 0 || errOut.Len() != 0 {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	text := out.String()
	for _, want := range []string{
		"AI providers",
		"PROVIDER     HUMANSH NAME  STATUS",
		"? Codex        codex",
		"Installed — live check pending",
		"Next: humansh provider test codex",
		"Claude Code  claude",
		"Next: humansh provider test claude",
		"? Cursor CLI   cursor",
		"Next: humansh provider test cursor",
		"(current)",
		"OpenRouter   openrouter",
		"Not configured — metered",
		"Next: humansh provider configure openrouter",
		"Switch:     humansh provider use <name>",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("provider list missing %q:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"installed=", "configured=", "authenticated=", "usable=", "auth login", "login status"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("provider list exposed raw diagnostic %q:\n%s", unwanted, text)
		}
	}
	previous := -1
	for _, name := range []string{"Codex", "Claude Code", "Cursor CLI", "OpenRouter"} {
		index := strings.Index(text, name)
		if index <= previous {
			t.Errorf("provider ordering is unclear: %q index=%d previous=%d\n%s", name, index, previous, text)
		}
		previous = index
	}
}

func TestProviderListJSONIncludesCurrentAndCompleteDiagnostics(t *testing.T) {
	t.Parallel()
	provider := &setupTestProvider{id: llm.Claude, diagnostic: llm.Diagnostic{
		Installed: true, Configured: true, Authenticated: true, Available: true, LiveCheck: true, AuthMode: "provider_managed", Executable: "/selected/claude",
	}}
	cfg := config.Default()
	cfg.Provider = llm.Claude
	rt := bootstrap.Runtime{Engine: app.Engine{Providers: llm.MapRegistry{llm.Claude: provider}}, Config: cfg}
	var out, errOut bytes.Buffer

	code := runProvider(context.Background(), []string{"list", "--json"}, rt, IO{Out: &out, Err: &errOut})
	if code != 0 || errOut.Len() != 0 {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	var result providerListResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("invalid provider list JSON: %v\n%s", err, out.String())
	}
	if result.Current != llm.Claude || len(result.Providers) != 1 || !result.Providers[0].Current || result.Providers[0].Diagnostic.Executable != "/selected/claude" {
		t.Fatalf("unexpected provider list JSON: %+v", result)
	}
}

func TestSetupUsesOneLiveProbeWithoutOpeningLogin(t *testing.T) {
	ready := llm.Diagnostic{Installed: true, Configured: true, Authenticated: true, Available: true, AuthMode: "provider_managed", Message: "Codex responded to a minimal inference prompt"}
	provider := &setupTestProvider{
		id:              llm.Codex,
		diagnostic:      llm.Diagnostic{Installed: true, Configured: true, AuthMode: "provider_managed", Message: "live inference has not been checked"},
		probeDiagnostic: &ready,
	}
	rt := bootstrap.Runtime{Engine: app.Engine{Providers: llm.MapRegistry{llm.Codex: provider}}, Config: config.Default()}
	var out, errOut bytes.Buffer
	selected, ok, code := selectSetupProvider(context.Background(), rt, "codex", false, true, IO{In: strings.NewReader(""), Out: &out, Err: &errOut})
	if code != 0 || !ok || selected != llm.Codex || provider.probeCalls != 1 {
		t.Fatalf("selected=%s ok=%t code=%d probes=%d out=%s err=%s", selected, ok, code, provider.probeCalls, out.String(), errOut.String())
	}
	for _, unwanted := range []string{"login", "auth status", "subscription"} {
		if strings.Contains(strings.ToLower(out.String()+errOut.String()), unwanted) {
			t.Fatalf("setup mentioned unsupported %q flow:\n%s%s", unwanted, out.String(), errOut.String())
		}
	}
}

func TestInteractiveSetupRetriesSelectedProviderAfterUserFix(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	tests := []struct {
		name             string
		explicit         string
		input            string
		wantProviderMenu int
	}{
		{name: "provider menu selection", input: "\n\n", wantProviderMenu: 1},
		{name: "explicit provider", explicit: "cursor", input: "\n", wantProviderMenu: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			failed := llm.Diagnostic{
				Installed: true, Configured: true, AuthMode: "provider_managed",
				Message: "Cursor CLI reported: authentication must be repaired",
			}
			ready := llm.Diagnostic{
				Installed: true, Configured: true, Authenticated: true, Available: true, AuthMode: "provider_managed",
				Message: "Cursor CLI responded after authentication was repaired",
			}
			provider := &setupTestProvider{
				id:         llm.Cursor,
				diagnostic: llm.Diagnostic{Installed: true, Configured: true, AuthMode: "provider_managed"},
			}
			provider.probeFunc = func(context.Context) llm.Diagnostic {
				if provider.probeCalls == 1 {
					return failed
				}
				return ready
			}
			cfg := config.Default()
			cfg.Provider = llm.Cursor
			rt := bootstrap.Runtime{Engine: app.Engine{Providers: llm.MapRegistry{llm.Cursor: provider}}, Config: cfg}
			var pending *setupOpenRouterCredential
			var out, errOut bytes.Buffer
			ui := interactiveSetupUI(test.input, &out, &errOut)

			selected, ok, code := configureSetupProvider(context.Background(), &rt, &cfg, test.explicit, false, ui, &pending)
			if selected != llm.Cursor || !ok || code != 0 || provider.probeCalls != 2 {
				t.Fatalf("selected=%s ok=%t code=%d probes=%d out=%s err=%s", selected, ok, code, provider.probeCalls, out.String(), errOut.String())
			}
			text := out.String()
			for _, want := range []string{"authentication must be repaired", "Fix the issue above", "Retry Cursor CLI? [Y/n]", "Using Cursor CLI"} {
				if !strings.Contains(text, want) {
					t.Errorf("retry flow missing %q:\n%s", want, text)
				}
			}
			if count := strings.Count(text, "AI provider ["); count != test.wantProviderMenu {
				t.Errorf("provider menu count=%d want=%d:\n%s", count, test.wantProviderMenu, text)
			}
			if strings.Contains(text, "Choose a different provider?") || errOut.Len() != 0 {
				t.Fatalf("retry flow used the old recovery prompt or wrote stderr:\n%s%s", text, errOut.String())
			}
		})
	}
}

func TestInteractiveSetupDeclinesRetryAndReturnsToProviderList(t *testing.T) {
	failed := llm.Diagnostic{
		Installed: true, Configured: true, AuthMode: "provider_managed",
		Message: "Codex reported: authentication required",
	}
	codex := &setupTestProvider{
		id:              llm.Codex,
		diagnostic:      llm.Diagnostic{Installed: true, Configured: true, AuthMode: "provider_managed"},
		probeDiagnostic: &failed,
	}
	openRouter := &setupTestProvider{
		id:         llm.OpenRouter,
		diagnostic: llm.Diagnostic{Installed: true, Configured: true, Authenticated: true, Available: true, LiveCheck: true, AuthMode: "api_key"},
	}
	rt := bootstrap.Runtime{
		Engine: app.Engine{Providers: llm.MapRegistry{llm.Codex: codex, llm.OpenRouter: openRouter}},
		Config: config.Default(),
	}
	cfg := rt.Config
	var pending *setupOpenRouterCredential
	var out, errOut bytes.Buffer
	ui := interactiveSetupUI("\nn\n4\n", &out, &errOut)

	selected, ok, code := configureSetupProvider(context.Background(), &rt, &cfg, "", false, ui, &pending)
	if selected != llm.OpenRouter || !ok || code != 0 || codex.probeCalls != 1 {
		t.Fatalf("selected=%s ok=%t code=%d probes=%d out=%s err=%s", selected, ok, code, codex.probeCalls, out.String(), errOut.String())
	}
	text := out.String()
	if count := strings.Count(text, "AI provider ["); count != 2 {
		t.Fatalf("provider menu count=%d want=2:\n%s", count, text)
	}
	for _, want := range []string{"Retry Codex? [Y/n]", "Answer no to return to the provider list", "Using OpenRouter"} {
		if !strings.Contains(text, want) {
			t.Errorf("provider fallback flow missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Setup stopped") || errOut.Len() != 0 {
		t.Fatalf("declining a retry stopped setup instead of returning to the provider list:\n%s%s", text, errOut.String())
	}
}

func TestSetupCancellationStopsActiveProviderProbe(t *testing.T) {
	started := make(chan struct{})
	provider := &setupTestProvider{
		id:         llm.Codex,
		diagnostic: llm.Diagnostic{Installed: true, Configured: true, AuthMode: "provider_managed"},
		probeFunc: func(ctx context.Context) llm.Diagnostic {
			close(started)
			<-ctx.Done()
			return llm.Diagnostic{Installed: true, Configured: true, AuthMode: "provider_managed", Message: ctx.Err().Error()}
		},
	}
	rt := bootstrap.Runtime{Engine: app.Engine{Providers: llm.MapRegistry{llm.Codex: provider}}, Config: config.Default()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out, errOut bytes.Buffer
	type result struct {
		selected llm.ProviderID
		ok       bool
		code     int
	}
	finished := make(chan result, 1)
	go func() {
		selected, ok, code := selectSetupProvider(ctx, rt, "codex", false, true, IO{In: strings.NewReader(""), Out: &out, Err: &errOut})
		finished <- result{selected: selected, ok: ok, code: code}
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("provider probe did not start")
	}
	cancel()
	select {
	case got := <-finished:
		if got.selected != llm.Codex || got.ok || got.code != 130 || provider.probeCalls != 1 {
			t.Fatalf("selected=%s ok=%t code=%d probes=%d out=%s err=%s", got.selected, got.ok, got.code, provider.probeCalls, out.String(), errOut.String())
		}
		if combined := out.String() + errOut.String(); strings.Contains(combined, "is not ready") || strings.Contains(combined, "Live check failed") {
			t.Fatalf("cancellation was reported as a provider failure:\n%s", combined)
		}
	case <-time.After(time.Second):
		t.Fatal("setup did not return after provider probe cancellation")
	}
}

func TestSetupReturnsExactLiveProbeFailureWithoutAuthAdvice(t *testing.T) {
	failed := llm.Diagnostic{
		Installed: true, Configured: true, LiveCheck: true, AuthMode: "provider_managed",
		Executable: "/selected/claude",
		Message:    "Claude Code reported: organization policy denied inference",
		NextSteps:  []llm.DiagnosticAction{{Description: "Check", Command: "humansh provider test claude"}},
	}
	provider := &setupTestProvider{
		id:              llm.Claude,
		diagnostic:      llm.Diagnostic{Installed: true, Configured: true, AuthMode: "provider_managed", Executable: "/selected/claude"},
		probeDiagnostic: &failed,
	}
	rt := bootstrap.Runtime{Engine: app.Engine{Providers: llm.MapRegistry{llm.Claude: provider}}, Config: config.Default()}
	var out, errOut bytes.Buffer
	_, ok, code := selectSetupProvider(context.Background(), rt, "claude", false, true, IO{In: strings.NewReader(""), Out: &out, Err: &errOut})
	if code != protocol.ExitProviderUnavailable || ok || provider.probeCalls != 1 {
		t.Fatalf("ok=%t code=%d probes=%d out=%s err=%s", ok, code, provider.probeCalls, out.String(), errOut.String())
	}
	combined := out.String() + errOut.String()
	for _, want := range []string{`Executable "/selected/claude"`, "organization policy denied inference", "humansh provider test claude"} {
		if !strings.Contains(combined, want) {
			t.Errorf("provider guidance missing %q:\n%s", want, combined)
		}
	}
	for _, unwanted := range []string{"auth login", "Sign in to Claude"} {
		if strings.Contains(combined, unwanted) {
			t.Errorf("provider guidance prescribed unsupported auth flow %q:\n%s", unwanted, combined)
		}
	}
}

func TestInteractiveSetupAlwaysAsksWhichProviderToUse(t *testing.T) {
	codexProvider := &setupTestProvider{id: llm.Codex, diagnostic: llm.Diagnostic{Installed: true, Available: true, Authenticated: true, AuthMode: "provider_managed"}}
	claudeProvider := &setupTestProvider{id: llm.Claude, diagnostic: llm.Diagnostic{Installed: true, Available: true, Authenticated: true, AuthMode: "provider_managed"}}
	rt := bootstrap.Runtime{Engine: app.Engine{Providers: llm.MapRegistry{llm.Codex: codexProvider, llm.Claude: claudeProvider}}, Config: config.Default()}
	var out, errOut bytes.Buffer
	selected, ok, code := selectSetupProvider(context.Background(), rt, "", false, true, IO{In: strings.NewReader("2\n"), Out: &out, Err: &errOut})
	if code != 0 || !ok || selected != llm.Claude {
		t.Fatalf("selected=%s ok=%t code=%d out=%s err=%s", selected, ok, code, out.String(), errOut.String())
	}
	for _, want := range []string{"1  Codex", "2  Claude Code", "3  Cursor CLI", "4  OpenRouter", "(current)", "AI provider [1]", "Using Claude Code"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("provider choice missing %q:\n%s", want, out.String())
		}
	}
}

func TestSetupHidesUnselectedProviderDiagnostics(t *testing.T) {
	codexProvider := &setupTestProvider{id: llm.Codex, diagnostic: llm.Diagnostic{Installed: true, Available: true, Authenticated: true, AuthMode: "provider_managed"}}
	claudeProvider := &setupTestProvider{id: llm.Claude, diagnostic: llm.Diagnostic{
		Installed:  true,
		Configured: true,
		AuthMode:   "provider_managed",
		Executable: "/hidden/claude",
		Message:    "private corporate gateway diagnostic",
		NextSteps:  []llm.DiagnosticAction{{Description: "Vendor recovery", Command: "/hidden/vendor-repair"}},
	}}
	openRouterProvider := &setupTestProvider{id: llm.OpenRouter, diagnostic: llm.Diagnostic{Installed: true, AuthMode: "missing", Message: "OpenRouter API key is not configured"}}
	rt := bootstrap.Runtime{Engine: app.Engine{Providers: llm.MapRegistry{llm.Codex: codexProvider, llm.Claude: claudeProvider, llm.OpenRouter: openRouterProvider}}, Config: config.Default()}
	var out, errOut bytes.Buffer
	selected, ok, code := selectSetupProvider(context.Background(), rt, "", false, true, IO{In: strings.NewReader("\n"), Out: &out, Err: &errOut})
	if code != 0 || !ok || selected != llm.Codex {
		t.Fatalf("selected=%s ok=%t code=%d out=%s err=%s", selected, ok, code, out.String(), errOut.String())
	}
	for _, hidden := range []string{"/hidden/claude", "private corporate gateway diagnostic", "/hidden/vendor-repair", "OpenRouter API key"} {
		if strings.Contains(out.String(), hidden) {
			t.Errorf("unselected diagnostic %q leaked into setup:\n%s", hidden, out.String())
		}
	}
	if !strings.Contains(out.String(), "2  Claude Code   Installed — live check pending") {
		t.Fatalf("compact Claude status did not distinguish a pending live check:\n%s", out.String())
	}
}

// TestConfigListKeysRoundTripThroughConfigGet keeps `config list` emitting the
// same key names `config get` and `config set` accept. Listing the runtime
// struct instead would print Go field names and raw nanosecond durations, so a
// user who discovered a key from `list` could not feed it back to `get`.
func TestConfigListKeysRoundTripThroughConfigGet(t *testing.T) {
	isolatedEnv(t)
	var out, errOut bytes.Buffer
	if code := Run(context.Background(), []string{"config", "list"}, IO{In: strings.NewReader(""), Out: &out, Err: &errOut}); code != 0 {
		t.Fatalf("config list code=%d err=%s", code, errOut.String())
	}
	var listed map[string]string
	if err := json.Unmarshal(out.Bytes(), &listed); err != nil {
		t.Fatalf("config list is not a flat key/value object: %v: %s", err, out.String())
	}
	if len(listed) != len(configKeys) {
		t.Fatalf("config list emitted %d keys, want %d", len(listed), len(configKeys))
	}
	for key := range listed {
		var getOut, getErr bytes.Buffer
		if code := Run(context.Background(), []string{"config", "get", key}, IO{In: strings.NewReader(""), Out: &getOut, Err: &getErr}); code != 0 {
			t.Fatalf("config get %q returned %d: %s", key, code, getErr.String())
		}
	}
	if listed["timeout_seconds"] != "20" {
		t.Fatalf("timeout_seconds = %q, want seconds not a raw duration", listed["timeout_seconds"])
	}
	if listed["shell.clear_line_binding"] != "^[" {
		t.Fatalf("shell.clear_line_binding = %q, want Escape", listed["shell.clear_line_binding"])
	}
}

// TestConfigGetUnknownKeyExplainsItself covers the Section 17 rule that every
// user-facing failure says what failed and how to fix it; a bare exit status
// leaves the user with nothing to act on.
func TestConfigGetUnknownKeyExplainsItself(t *testing.T) {
	isolatedEnv(t)
	var out, errOut bytes.Buffer
	code := Run(context.Background(), []string{"config", "get", "Provider"}, IO{In: strings.NewReader(""), Out: &out, Err: &errOut})
	if code != 2 {
		t.Fatalf("code=%d, want 2", code)
	}
	message := errOut.String()
	if !strings.Contains(message, "not a supported configuration key") || !strings.Contains(message, "provider") || !strings.Contains(message, "humansh config list") {
		t.Fatalf("unknown-key error is not actionable: %q", message)
	}
}

func TestClaudeBinaryConfigCanBePinnedAndRestoredToAuto(t *testing.T) {
	cfg := config.Default()
	const path = "/opt/homebrew/bin/claude"
	if err := configSet(&cfg, "providers.claude.binary", path); err != nil {
		t.Fatal(err)
	}
	if got, ok := configGet(cfg, "providers.claude.binary"); !ok || got != path {
		t.Fatalf("pinned get=(%q,%t)", got, ok)
	}
	if err := configSet(&cfg, "providers.claude.binary", "auto"); err != nil {
		t.Fatal(err)
	}
	if got, ok := configGet(cfg, "providers.claude.binary"); !ok || got != "auto" || cfg.Claude.Binary != "" {
		t.Fatalf("automatic get=(%q,%t) config=%q", got, ok, cfg.Claude.Binary)
	}
}

func TestCursorBinaryConfigCanBePinnedAndRestoredToAuto(t *testing.T) {
	cfg := config.Default()
	const path = "/opt/homebrew/bin/cursor-agent"
	if err := configSet(&cfg, "providers.cursor.binary", path); err != nil {
		t.Fatal(err)
	}
	if got, ok := configGet(cfg, "providers.cursor.binary"); !ok || got != path {
		t.Fatalf("pinned get=(%q,%t)", got, ok)
	}
	if err := configSet(&cfg, "providers.cursor.binary", "auto"); err != nil {
		t.Fatal(err)
	}
	if got, ok := configGet(cfg, "providers.cursor.binary"); !ok || got != "auto" || cfg.Cursor.Binary != "" {
		t.Fatalf("automatic get=(%q,%t) config=%q", got, ok, cfg.Cursor.Binary)
	}
}
