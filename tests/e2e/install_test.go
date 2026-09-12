package e2e_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	listRequest             = "list all the files in this directory"
	shortListRequest        = "list files"
	listCommand             = "ls -la"
	shortListCommand        = "ls"
	ambiguousRMRequest      = "rm is not working"
	ambiguousRMCommand      = "man rm"
	findContentRequest      = `find the file with "ABC" content`
	createMarkerRequest     = "please create a marker file for me"
	createMarkerCommand     = "touch humansh-e2e-generated-marker"
	deleteTargetRequest     = "please delete the e2e target directory"
	deleteTargetCommand     = "rm -rf -- humansh-e2e-high-risk-target"
	providerFailureRequest  = "show me a provider failure"
	optionalToolRequest     = "use the locally installed optional tool"
	optionalToolCommand     = "humansh-e2e-optional --version"
	missingToolRequest      = "use a missing tool to show its version"
	zellijAttachCommand     = "zellij attach -c pyxis-codex -- codex"
	zellijExecutedOutput    = "HUMANSH_E2E_ZELLIJ_EXECUTED:<attach|-c|pyxis-codex|--|codex>"
	goCoverCommand          = "go test -cover"
	goHelpTestCommand       = "go help test"
	dockerRunCommand        = "docker run --rm --network host docker.io/library/node@sha256:6dac556d980b7f0e5498d08f08cee0ca67798b4ad6c23964a9214920e67758d0 curl --silent --show-error --fail --max-time 8 http://127.0.0.1:3000/api/v1/version"
	privateEnvironmentValue = "HUMANSH_E2E_ENV_SECRET_DO_NOT_SEND"
	privateFileValue        = "HUMANSH_E2E_FILE_SECRET_DO_NOT_SEND"
)

type installedFixture struct {
	root        string
	home        string
	providerBin string
	path        string
	callLog     string
	env         []string
}

type providerEvent struct {
	Event   string `json:"event"`
	Request string `json:"request"`
	PID     int    `json:"pid"`
}

func TestInstalledZshEndToEnd(t *testing.T) {
	if os.Getenv("HUMANSH_RUN_E2E") != "1" {
		t.Skip("set HUMANSH_RUN_E2E=1 to run the installed-flow tests")
	}
	for _, command := range []string{"go", "sh", "zsh"} {
		if _, err := exec.LookPath(command); err != nil {
			t.Fatalf("required E2E command %q is unavailable: %v", command, err)
		}
	}

	fixture := installZshFixture(t)

	t.Run("literal command executes without a provider call", func(t *testing.T) {
		fixture.runZshScenario(t, `
zpty -w -n H $'ls -la\r'
wait_for 'humansh-e2e-visible.txt' || exit 101
eventually_dump '' || exit 102
`)
		fixture.requireProviderEvents(t, "", nil)
	})

	t.Run("go test cover executes despite opaque subcommand flag help", func(t *testing.T) {
		output := fixture.runZshScenario(t, `
zpty -w -n H "$HUMANSH_E2E_COMMAND"$'\r'
wait_for 'no Go files in' || exit 138
eventually_dump '' || exit 139
`, "HUMANSH_E2E_COMMAND", goCoverCommand)
		if strings.Contains(output, "Not sure whether this is English or a command") {
			t.Fatalf("real Go command was left ambiguous:\n%s", output)
		}
		fixture.requireProviderEvents(t, "", nil)
	})

	t.Run("go help test executes as a documented help form", func(t *testing.T) {
		output := fixture.runZshScenario(t, `
zpty -w -n H "$HUMANSH_E2E_COMMAND"$'\r'
wait_for 'automates testing the packages named' || exit 140
eventually_dump '' || exit 141
`, "HUMANSH_E2E_COMMAND", goHelpTestCommand)
		if strings.Contains(output, "Not sure whether this is English or a command") {
			t.Fatalf("documented Go help command was left ambiguous:\n%s", output)
		}
		fixture.requireProviderEvents(t, "", nil)
	})

	t.Run("zellij attach with a session and initial command executes literally", func(t *testing.T) {
		fixture.runZshScenario(t, `
zpty -w -n H "$HUMANSH_E2E_COMMAND"$'\r'
wait_for "$HUMANSH_E2E_EXPECTED" || exit 136
eventually_dump '' || exit 137
`,
			"HUMANSH_E2E_COMMAND", zellijAttachCommand,
			"HUMANSH_E2E_EXPECTED", zellijExecutedOutput,
		)
		fixture.requireProviderEvents(t, "", nil)
	})

	t.Run("docker run forwards container command flags unchanged", func(t *testing.T) {
		output := fixture.runZshScenario(t, `
zpty -w -n H "$HUMANSH_E2E_COMMAND"$'\r'
wait_for 'HUMANSH_E2E_DOCKER_EXECUTED' || exit 150
eventually_dump '' || exit 151
`, "HUMANSH_E2E_COMMAND", dockerRunCommand)
		if strings.Contains(output, "Not sure whether this is English or a command") {
			t.Fatalf("container command flags were rejected as Docker options:\n%s", output)
		}
		fixture.requireDockerCalls(t, strings.Fields(dockerRunCommand)[1:])
		fixture.requireProviderEvents(t, "", nil)
	})

	t.Run("docker run still rejects an unknown outer option after a network value", func(t *testing.T) {
		const input = "docker run --network host --humansh-unknown-option node curl --silent"
		fixture.runZshScenario(t, `
zpty -w -n H "$HUMANSH_E2E_COMMAND"$'\r'
wait_for 'Not sure whether this is English or a command' || exit 152
dump_buffer "$HUMANSH_E2E_COMMAND" || exit 153
`, "HUMANSH_E2E_COMMAND", input)
		fixture.requireDockerCalls(t, nil)
		fixture.requireProviderEvents(t, "", nil)
	})

	for _, test := range []struct {
		name, input, executable string
		helpCalls               [][]string
	}{
		{"forwarded flags preserve English tails", "docker run node curl --help is failing please authenticate", "docker", [][]string{{"--help"}, {"run", "--help"}}},
		{"forwarded flags after an explicit separator preserve English tails", "docker run -- node curl --help is failing please authenticate", "docker", [][]string{{"--help"}, {"run", "--help"}}},
		{"quoted leading flags cannot establish an operand boundary", `docker run "--rm" --typo node curl`, "docker", [][]string{{"--help"}, {"run", "--help"}}},
		{"dynamic leading flags cannot establish an operand boundary", "docker run $HUMANSH_E2E_DOCKER_FLAGS --typo node curl", "docker", [][]string{{"--help"}, {"run", "--help"}}},
		{"conflicting synopses do not enable forwarding", "tool inspect readme --typo", "tool", [][]string{{"--help"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture.runZshScenario(t, `
zpty -w -n H "$HUMANSH_E2E_COMMAND"$'\r'
wait_for 'Not sure whether this is English or a command' 'HUMANSH_E2E_UNEXPECTED_EXECUTION' || exit 154
dump_buffer "$HUMANSH_E2E_COMMAND" || exit 155
`, "HUMANSH_E2E_COMMAND", test.input, "HUMANSH_E2E_DOCKER_FLAGS", "--rm")
			fixture.requireCommandCalls(t, test.executable, test.helpCalls)
			fixture.requireProviderEvents(t, "", nil)
		})
	}

	t.Run("natural language is translated for review and Escape clears it", func(t *testing.T) {
		fixture.runZshScenario(t, `
zpty -w -n H "$HUMANSH_E2E_REQUEST"$'\r'
wait_for 'Generated by Codex. Review it' || exit 103
dump_buffer "$HUMANSH_E2E_COMMAND" || exit 104
zpty -w -n H $'\x1b'
eventually_dump '' || exit 105
`, "HUMANSH_E2E_REQUEST", listRequest, "HUMANSH_E2E_COMMAND", listCommand)
		fixture.requireProviderEvents(t, listRequest, map[string]int{"started": 1, "completed": 1})
	})

	t.Run("short unresolved list request uses conventional ls", func(t *testing.T) {
		output := fixture.runZshScenario(t, `
zpty -w -n H "$HUMANSH_E2E_REQUEST"$'\r'
wait_for 'Generated by Codex. Review it' || exit 142
dump_buffer "$HUMANSH_E2E_COMMAND" || exit 143
`, "HUMANSH_E2E_REQUEST", shortListRequest, "HUMANSH_E2E_COMMAND", shortListCommand)
		if strings.Contains(output, "Not sure whether this is English or a command") {
			t.Fatalf("short unresolved request was left ambiguous:\n%s", output)
		}
		fixture.requireProviderEvents(t, shortListRequest, map[string]int{"started": 1, "completed": 1})
	})

	t.Run("arbitrary locally installed tool is accepted without a catalog or execution", func(t *testing.T) {
		tool := filepath.Join(fixture.providerBin, "humansh-e2e-optional")
		if err := os.WriteFile(tool, []byte("#!/bin/sh\nprintf executed > \"$0.executed\"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = os.Remove(tool)
			_ = os.Remove(tool + ".executed")
		})
		fixture.runZshScenario(t, `
zpty -w -n H "$HUMANSH_E2E_REQUEST"$'\r'
wait_for 'Generated by Codex. Review it' || exit 146
dump_buffer "$HUMANSH_E2E_COMMAND" || exit 147
[[ ! -e "$HUMANSH_E2E_MARKER" ]] || exit 148
`,
			"HUMANSH_E2E_REQUEST", optionalToolRequest,
			"HUMANSH_E2E_COMMAND", optionalToolCommand,
			"HUMANSH_E2E_MARKER", tool+".executed",
		)
		fixture.requireProviderEvents(t, optionalToolRequest, map[string]int{"started": 1, "completed": 1})
	})

	t.Run("missing generated executable is rejected and preserves the request", func(t *testing.T) {
		fixture.runZshScenario(t, `
zpty -w -n H "$HUMANSH_E2E_REQUEST"$'\r'
wait_for 'requires an unavailable executable' || exit 149
dump_buffer "$HUMANSH_E2E_REQUEST" || exit 150
`, "HUMANSH_E2E_REQUEST", missingToolRequest)
		fixture.requireProviderEvents(t, missingToolRequest, map[string]int{"started": 1, "completed": 1})
	})

	t.Run("the same short input executes when its first word resolves", func(t *testing.T) {
		listBinary := filepath.Join(fixture.providerBin, "list")
		contents := "#!/bin/sh\nif [ \"${1-}\" = \"--help\" ]; then\n  printf '%s\\n' 'Usage: list [FILE...]'\n  exit 0\nfi\nprintf '%s\\n' 'HUMANSH_E2E_REAL_LIST'\n"
		if err := os.WriteFile(listBinary, []byte(contents), 0o700); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Remove(listBinary) })

		output := fixture.runZshScenario(t, `
zpty -w -n H "$HUMANSH_E2E_REQUEST"$'\r'
wait_for 'HUMANSH_E2E_REAL_LIST' || exit 144
eventually_dump '' || exit 145
`, "HUMANSH_E2E_REQUEST", shortListRequest)
		if strings.Contains(output, "Generated by Codex") || strings.Contains(output, "Not sure whether this is English or a command") {
			t.Fatalf("resolved list command was not executed literally:\n%s", output)
		}
		fixture.requireProviderEvents(t, "", nil)
	})

	t.Run("ambiguous input can be forced to execute unchanged", func(t *testing.T) {
		fixture.runZshScenario(t, `
zpty -w -n H "$HUMANSH_E2E_REQUEST"$'\r'
wait_for 'Not sure whether this is English or a command' || exit 106
dump_buffer "$HUMANSH_E2E_REQUEST" || exit 107
zpty -w -n H $'\x18\r'
wait_for 'No such file or directory' || exit 108
eventually_dump '' || exit 109
`, "HUMANSH_E2E_REQUEST", ambiguousRMRequest)
		fixture.requireProviderEvents(t, "", nil)
	})

	t.Run("ambiguous input can be forced through translation", func(t *testing.T) {
		fixture.runZshScenario(t, `
zpty -w -n H "$HUMANSH_E2E_REQUEST"$'\r'
wait_for 'Not sure whether this is English or a command' || exit 110
[[ ! -s "$HUMANSH_E2E_CALL_LOG" ]] || exit 111
zpty -w -n H $'\x07'
wait_for 'Generated by Codex. Review it' || exit 112
dump_buffer "$HUMANSH_E2E_COMMAND" || exit 113
`, "HUMANSH_E2E_REQUEST", ambiguousRMRequest, "HUMANSH_E2E_COMMAND", ambiguousRMCommand)
		fixture.requireProviderEvents(t, ambiguousRMRequest, map[string]int{"started": 1, "completed": 1})
	})

	t.Run("Escape clears an unsubmitted request without a provider call", func(t *testing.T) {
		fixture.runZshScenario(t, `
zpty -w -n H "$HUMANSH_E2E_REQUEST"
dump_buffer "$HUMANSH_E2E_REQUEST" || exit 113
zpty -w -n H $'\x1b'
eventually_dump '' || exit 114
`, "HUMANSH_E2E_REQUEST", findContentRequest)
		fixture.requireProviderEvents(t, "", nil)
	})

	t.Run("Escape cancels an active translation and clears the request", func(t *testing.T) {
		fixture.runZshScenario(t, `
zpty -w -n H "$HUMANSH_E2E_REQUEST"$'\x07'
wait_for 'Translating with Codex' || exit 115
wait_for_provider_start || exit 116
zpty -w -n H $'\x1b'
eventually_dump '' || exit 117
`, "HUMANSH_E2E_REQUEST", findContentRequest)
		events := fixture.requireProviderEvents(t, findContentRequest, map[string]int{"started": 1})
		assertProviderStopped(t, events)
	})

	t.Run("Ctrl-C cancels an active translation and restores the request", func(t *testing.T) {
		fixture.runZshScenario(t, `
zpty -w -n H "$HUMANSH_E2E_REQUEST"$'\x07'
wait_for 'Translating with Codex' || exit 118
wait_for_provider_start || exit 119
zpty -w -n H $'\x03'
wait_for 'Translation cancelled; your original text is restored' || exit 120
dump_buffer "$HUMANSH_E2E_REQUEST" || exit 121
`, "HUMANSH_E2E_REQUEST", findContentRequest)
		events := fixture.requireProviderEvents(t, findContentRequest, map[string]int{"started": 1})
		assertProviderStopped(t, events)
	})

	t.Run("generated command waits for review before execution", func(t *testing.T) {
		marker := filepath.Join(fixture.root, "humansh-e2e-generated-marker")
		if err := os.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		output := fixture.runZshScenario(t, `
zpty -w -n H "$HUMANSH_E2E_REQUEST"$'\r'
wait_for 'Generated by Codex. Review it' || exit 122
[[ ! -e "$HUMANSH_E2E_MARKER" ]] || exit 123
sleep 0.05
zpty -w -n H $'\r'
wait_for_path "$HUMANSH_E2E_MARKER" || exit 124
eventually_dump '' || exit 125
`,
			"HUMANSH_E2E_REQUEST", createMarkerRequest,
			"HUMANSH_E2E_MARKER", marker,
		)
		if !strings.Contains(output, createMarkerCommand) {
			t.Fatalf("generated command was not shown for review:\n%s", output)
		}
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("reviewed generated command did not run in the interactive shell: %v", err)
		}
		fixture.requireProviderEvents(t, createMarkerRequest, map[string]int{"started": 1, "completed": 1})
	})

	t.Run("high-risk generated command requires Ctrl-X then Enter", func(t *testing.T) {
		target := filepath.Join(fixture.root, "humansh-e2e-high-risk-target")
		if err := os.MkdirAll(target, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, "keep-until-confirmed.txt"), []byte("still here\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		fixture.runZshScenario(t, `
zpty -w -n H "$HUMANSH_E2E_REQUEST"$'\r'
wait_for 'High-risk generated command' || exit 127
dump_buffer "$HUMANSH_E2E_COMMAND" || exit 128
[[ -d "$HUMANSH_E2E_TARGET" ]] || exit 129
reset_window
zpty -w -n H $'\r'
wait_for 'Next: press Ctrl-X then Enter to run it' || exit 130
[[ -d "$HUMANSH_E2E_TARGET" ]] || exit 131
zpty -w -n H $'\x18\r'
eventually_dump '' || exit 132
wait_for_missing_path "$HUMANSH_E2E_TARGET" || exit 133
`,
			"HUMANSH_E2E_REQUEST", deleteTargetRequest,
			"HUMANSH_E2E_COMMAND", deleteTargetCommand,
			"HUMANSH_E2E_TARGET", target,
		)
		if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("high-risk command target still exists or could not be inspected: %v", err)
		}
		fixture.requireProviderEvents(t, deleteTargetRequest, map[string]int{"started": 1, "completed": 1})
	})

	t.Run("provider failure preserves the original request", func(t *testing.T) {
		fixture.runZshScenario(t, `
zpty -w -n H "$HUMANSH_E2E_REQUEST"$'\r'
wait_for 'Codex could not complete the translation' || exit 134
dump_buffer "$HUMANSH_E2E_REQUEST" || exit 135
`, "HUMANSH_E2E_REQUEST", providerFailureRequest)
		fixture.requireProviderEvents(t, providerFailureRequest, map[string]int{"started": 1, "failed": 1})
	})
}

func installZshFixture(t *testing.T) *installedFixture {
	t.Helper()
	repo := repositoryRoot(t)
	testRoot := t.TempDir()
	home := filepath.Join(testRoot, "home")
	providerBin := filepath.Join(testRoot, "provider-bin")
	for _, directory := range []string{home, providerBin} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(testRoot, "humansh-e2e-visible.txt"), []byte("listed by the literal-command scenario\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(testRoot, "private-source.txt"), []byte(privateFileValue+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(testRoot, "go.mod"), []byte("module humansh-e2e-empty\n\ngo 1.22\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	fakeCodex := filepath.Join(providerBin, "codex")
	buildFixture := exec.Command("go", "build", "-trimpath", "-o", fakeCodex, "./tests/e2e/testdata/fakecodex")
	buildFixture.Dir = repo
	if output, err := buildFixture.CombinedOutput(); err != nil {
		t.Fatalf("build deterministic Codex fixture: %v\n%s", err, output)
	}
	fakeZellij := filepath.Join(providerBin, "zellij")
	buildFixture = exec.Command("go", "build", "-trimpath", "-o", fakeZellij, "./tests/e2e/testdata/fakezellij")
	buildFixture.Dir = repo
	if output, err := buildFixture.CombinedOutput(); err != nil {
		t.Fatalf("build deterministic Zellij fixture: %v\n%s", err, output)
	}
	fakeDocker := filepath.Join(providerBin, "docker")
	buildFixture = exec.Command("go", "build", "-trimpath", "-o", fakeDocker, "./tests/e2e/testdata/fakedocker")
	buildFixture.Dir = repo
	if output, err := buildFixture.CombinedOutput(); err != nil {
		t.Fatalf("build deterministic Docker fixture: %v\n%s", err, output)
	}
	fixtureData, err := os.ReadFile(fakeDocker)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(providerBin, "tool"), fixtureData, 0o700); err != nil {
		t.Fatal(err)
	}

	env := isolatedEnvironment(t, home, providerBin)
	installer := exec.Command("sh", filepath.Join(repo, "scripts", "install.sh"), "--local", "--shell", "zsh")
	installer.Dir = repo
	installer.Env = env
	installerOutput, err := installer.CombinedOutput()
	if err != nil {
		t.Fatalf("install checked-out Humansh: %v\n%s", err, installerOutput)
	}
	if text := string(installerOutput); !strings.Contains(text, "setup --shell zsh") {
		t.Fatalf("installer did not complete the normal non-interactive flow:\n%s", text)
	} else {
		result := "Installation details\n\n" +
			"  Binary     ~/.local/bin/humansh\n" +
			"  License    MIT"
		completionIndex := strings.Index(text, "Installation details\n")
		if completionIndex < 0 {
			t.Fatalf("installer did not print a completion section:\n%s", text)
		}
		completion := text[completionIndex:]
		if !strings.Contains(completion, result) {
			t.Fatalf("installer did not print the aligned completion result:\n%s", text)
		}
		if strings.Contains(completion, home) || strings.Contains(completion, "github.com/agenticlab-ai/humansh/blob/main/LICENSE") {
			t.Fatalf("installer printed a long home or license path:\n%s", text)
		}
	}

	installedBinary := filepath.Join(home, ".local", "bin", "humansh")
	if info, err := os.Stat(installedBinary); err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("installed binary is unavailable or not executable: info=%v err=%v", info, err)
	}

	setup := exec.Command(installedBinary, "setup", "--yes", "--provider", "codex", "--shell", "zsh")
	setup.Env = env
	setup.Stdin = strings.NewReader("")
	setupOutput, err := setup.CombinedOutput()
	if err != nil {
		t.Fatalf("complete installed Zsh setup: %v\n%s", err, setupOutput)
	}
	if !strings.Contains(string(setupOutput), "🎉 Humansh is ready!") {
		t.Fatalf("setup did not report completion:\n%s", setupOutput)
	}

	appendZLEProbe(t, filepath.Join(home, ".zshrc"))
	return &installedFixture{
		root:        testRoot,
		home:        home,
		providerBin: providerBin,
		path:        environmentValue(env, "PATH"),
		callLog:     filepath.Join(providerBin, "humansh-e2e-calls.jsonl"),
		env:         env,
	}
}

func appendZLEProbe(t *testing.T, zshrc string) {
	t.Helper()
	file, err := os.OpenFile(zshrc, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatalf("open installed .zshrc: %v", err)
	}
	const probe = `
# Test-only observation widget. The installed Humansh block above remains the
# code under test; this widget only exposes the editable ZLE buffer to the
# parent test without accepting or executing it.
bindkey -e
PS1='HUMANSH_E2E_PROMPT> '
_humansh_e2e_dump_buffer() {
  zle -I
  print -r -- "HUMANSH_E2E_BUFFER:<${BUFFER}>"
  zle reset-prompt
}
zle -N _humansh_e2e_dump_buffer
bindkey -M emacs '^]' _humansh_e2e_dump_buffer
print -r -- "HUMANSH_E2E_READY:$(bindkey -M emacs '^M')"
`
	if _, err := file.WriteString(probe); err != nil {
		_ = file.Close()
		t.Fatalf("append ZLE observation widget: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close installed .zshrc: %v", err)
	}
}

func (fixture *installedFixture) runZshScenario(t *testing.T, body string, variables ...string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(fixture.callLog), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.callLog, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"docker", "tool"} {
		if err := os.WriteFile(filepath.Join(fixture.providerBin, name+".calls.jsonl"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { fixture.cleanupProviderProcesses(t) })

	const prelude = `zmodload zsh/zpty || exit 90
zpty -b H env TERM=xterm-256color HOME="$HUMANSH_E2E_HOME" ZDOTDIR="$HUMANSH_E2E_HOME" PATH="$HUMANSH_E2E_PATH" HUMANSH_E2E_PRIVATE_ENV="$HUMANSH_E2E_PRIVATE_ENV" zsh -d -i
typeset -g HUMANSH_E2E_ALL=''
typeset -g HUMANSH_E2E_WINDOW=''
drain_output() {
  local chunk
  while zpty -r -t H chunk; do
    HUMANSH_E2E_ALL+=$chunk
    HUMANSH_E2E_WINDOW+=$chunk
  done
}
reset_window() {
  drain_output
  HUMANSH_E2E_WINDOW=''
}
wait_for() {
  local pattern=$1 rejected=${2:-}
  local -i attempt
  for (( attempt = 1; attempt <= 1500; attempt++ )); do
    drain_output
    if [[ -n $rejected && $HUMANSH_E2E_WINDOW == *"${rejected}"* ]]; then
      print -ru2 -- "unexpected ${rejected}; received ${(V)HUMANSH_E2E_ALL}"
      return 1
    fi
    [[ $HUMANSH_E2E_WINDOW == *"${pattern}"* ]] && return 0
    sleep 0.02
  done
  print -ru2 -- "missing ${pattern}; received ${(V)HUMANSH_E2E_ALL}"
  return 1
}
wait_for_provider_start() {
  local -i attempt
  for (( attempt = 1; attempt <= 500; attempt++ )); do
    drain_output
    [[ -s $HUMANSH_E2E_CALL_LOG ]] && return 0
    sleep 0.02
  done
  print -ru2 -- "provider did not start; received ${(V)HUMANSH_E2E_ALL}"
  return 1
}
dump_buffer() {
  local expected=$1 marker="HUMANSH_E2E_BUFFER:<${1}>"
  reset_window
  zpty -w -n H $'\x1d'
  wait_for "$marker" || return 1
  # The observation widget prints before it returns to ZLE. Let reset-prompt
  # finish before a scenario sends its next user key.
  sleep 0.05
  drain_output
}
eventually_dump() {
  local expected=$1 marker="HUMANSH_E2E_BUFFER:<${1}>"
  local -i attempt
  reset_window
  for (( attempt = 1; attempt <= 500; attempt++ )); do
    zpty -w -n H $'\x1d'
    sleep 0.02
    drain_output
    if [[ $HUMANSH_E2E_WINDOW == *"${marker}"* ]]; then
      return 0
    fi
  done
  print -ru2 -- "missing buffer ${expected}; received ${(V)HUMANSH_E2E_ALL}"
  return 1
}
wait_for_path() {
  local target=$1
  local -i attempt
  for (( attempt = 1; attempt <= 500; attempt++ )); do
    drain_output
    [[ -e $target ]] && return 0
    sleep 0.02
  done
  print -ru2 -- "path was not created: $target; received ${(V)HUMANSH_E2E_ALL}"
  return 1
}
wait_for_missing_path() {
  local target=$1
  local -i attempt
  for (( attempt = 1; attempt <= 500; attempt++ )); do
    drain_output
    [[ ! -e $target ]] && return 0
    sleep 0.02
  done
  print -ru2 -- "path was not removed: $target; received ${(V)HUMANSH_E2E_ALL}"
  return 1
}
wait_for 'HUMANSH_E2E_READY:' || exit 91
`
	const trailer = `
drain_output
print -r -- "$HUMANSH_E2E_ALL"
print -r -- HUMANSH_E2E_DONE
zpty -d H
`

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "zsh", "-f", "-c", prelude+body+trailer)
	command.Dir = fixture.root
	command.Env = setEnvironment(fixture.env,
		"HUMANSH_E2E_HOME", fixture.home,
		"HUMANSH_E2E_PATH", fixture.path,
		"HUMANSH_E2E_CALL_LOG", fixture.callLog,
	)
	command.Env = setEnvironment(command.Env, variables...)
	output, err := command.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			t.Fatalf("installed Zsh scenario exceeded its timeout:\n%s", output)
		}
		t.Fatalf("installed Zsh scenario failed: %v\n%s", err, output)
	}
	text := string(output)
	for _, want := range []string{"HUMANSH_E2E_READY:", "_humansh_smart_enter", "HUMANSH_E2E_DONE"} {
		if !strings.Contains(text, want) {
			t.Fatalf("interactive installed flow did not produce %q:\n%s", want, text)
		}
	}
	return text
}

func (fixture *installedFixture) requireDockerCalls(t *testing.T, executed []string) {
	t.Helper()
	want := [][]string{{"--help"}, {"run", "--help"}}
	if executed != nil {
		want = append(want, executed)
	}
	fixture.requireCommandCalls(t, "docker", want)
}

func (fixture *installedFixture) requireCommandCalls(t *testing.T, name string, want [][]string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixture.providerBin, name+".calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var args []string
		if err := json.Unmarshal([]byte(line), &args); err != nil {
			t.Fatalf("decode %s fixture call: %v", name, err)
		}
		calls = append(calls, args)
	}
	gotJSON, _ := json.Marshal(calls)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("%s calls=%s, want %s; only fixed help probes and the accepted original command may run", name, gotJSON, wantJSON)
	}
}

func (fixture *installedFixture) requireProviderEvents(t *testing.T, request string, want map[string]int) []providerEvent {
	t.Helper()
	events := fixture.readProviderEvents(t)
	if len(want) == 0 {
		if len(events) != 0 {
			t.Fatalf("provider was called unexpectedly: %+v", events)
		}
		return events
	}

	counts := map[string]int{}
	for _, event := range events {
		if event.Request != request {
			t.Fatalf("provider received request %q, want %q: %+v", event.Request, request, events)
		}
		counts[event.Event]++
	}
	for _, eventName := range []string{"started", "completed", "failed"} {
		if counts[eventName] != want[eventName] {
			t.Fatalf("provider event %q count=%d, want %d: %+v", eventName, counts[eventName], want[eventName], events)
		}
	}
	return events
}

func (fixture *installedFixture) readProviderEvents(t *testing.T) []providerEvent {
	t.Helper()
	file, err := os.Open(fixture.callLog)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		t.Fatal(err)
	}
	defer file.Close()

	var events []providerEvent
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event providerEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("decode fake provider event %q: %v", scanner.Text(), err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

func (fixture *installedFixture) cleanupProviderProcesses(t *testing.T) {
	t.Helper()
	events := fixture.readProviderEvents(t)
	started := map[int]bool{}
	finished := map[int]bool{}
	for _, event := range events {
		switch event.Event {
		case "started":
			started[event.PID] = true
		case "completed", "failed":
			finished[event.PID] = true
		}
	}
	for pid := range started {
		if finished[pid] {
			continue
		}
		if err := syscall.Kill(pid, 0); err != nil && !errors.Is(err, syscall.EPERM) {
			continue
		}
		t.Errorf("fake provider process %d was still running after the scenario", pid)
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
			t.Errorf("terminate leaked fake provider process %d: %v", pid, err)
		}
	}
}

func assertProviderStopped(t *testing.T, events []providerEvent) {
	t.Helper()
	pid := 0
	for _, event := range events {
		if event.Event == "started" {
			pid = event.PID
			break
		}
	}
	if pid <= 0 {
		t.Fatalf("provider start event has no process ID: %+v", events)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := syscall.Kill(pid, 0); err == nil || errors.Is(err, syscall.EPERM) {
		t.Fatalf("cancelled provider process %d is still running", pid)
	}
}

func isolatedEnvironment(t *testing.T, home, providerBin string) []string {
	t.Helper()
	goEnvironmentCommand := exec.Command("go", "env", "GOCACHE", "GOMODCACHE", "GOPATH")
	output, err := goEnvironmentCommand.Output()
	if err != nil {
		t.Fatalf("read configured Go build paths: %v", err)
	}
	goPaths := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(goPaths) != 3 {
		t.Fatalf("go env returned %d build paths, want 3: %q", len(goPaths), output)
	}
	return setEnvironment(os.Environ(),
		"HOME", home,
		"ZDOTDIR", home,
		"XDG_CONFIG_HOME", filepath.Join(home, "config"),
		"XDG_DATA_HOME", filepath.Join(home, "data"),
		"XDG_CACHE_HOME", filepath.Join(home, "cache"),
		"CODEX_HOME", filepath.Join(home, "codex"),
		"HUMANSH_NONINTERACTIVE", "1",
		"HUMANSH_E2E_PRIVATE_ENV", privateEnvironmentValue,
		"OPENROUTER_API_KEY", "",
		"TERM", "xterm-256color",
		"PATH", providerBin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GOCACHE", goPaths[0],
		"GOMODCACHE", goPaths[1],
		"GOPATH", goPaths[2],
	)
}

func setEnvironment(environment []string, pairs ...string) []string {
	values := make(map[string]string, len(pairs)/2)
	order := make([]string, 0, len(pairs)/2)
	for index := 0; index < len(pairs); index += 2 {
		values[pairs[index]] = pairs[index+1]
		order = append(order, pairs[index])
	}
	out := make([]string, 0, len(environment)+len(order))
	for _, item := range environment {
		key, _, ok := strings.Cut(item, "=")
		if _, replace := values[key]; !ok || replace {
			continue
		}
		out = append(out, item)
	}
	for _, key := range order {
		out = append(out, key+"="+values[key])
	}
	return out
}

func environmentValue(environment []string, key string) string {
	prefix := key + "="
	for _, item := range environment {
		if strings.HasPrefix(item, prefix) {
			return strings.TrimPrefix(item, prefix)
		}
	}
	return ""
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(workingDirectory, "..", ".."))
}
