package installer_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReleaseInstallerDefaultsToPublishedGitHubRepository(t *testing.T) {
	if (runtime.GOOS != "darwin" && runtime.GOOS != "linux") || (runtime.GOARCH != "arm64" && runtime.GOARCH != "amd64") {
		t.Skip("release installer supports darwin/linux on arm64/amd64")
	}
	repo := repositoryRoot(t)
	home := t.TempDir()
	fixtures := t.TempDir()
	asset := fmt.Sprintf("humansh-%s-%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	archivePath, checksumPath := writeReleaseFixture(t, fixtures, asset)
	curlLog := filepath.Join(fixtures, "curl.log")
	fakeBin := filepath.Join(fixtures, "bin")
	if err := os.Mkdir(fakeBin, 0o700); err != nil {
		t.Fatal(err)
	}
	fakeCurl := `#!/bin/sh
set -eu
url=
output=
while [ "$#" -gt 0 ]; do
  case $1 in
    -o) output=$2; shift 2 ;;
    https://*) url=$1; shift ;;
    *) shift ;;
  esac
done
[ -n "$url" ] && [ -n "$output" ] || exit 2
printf '%s\n' "$url" >> "$HUMANSH_FAKE_CURL_LOG"
case $url in
  *.tar.gz.sha256) cp "$HUMANSH_FAKE_CHECKSUM" "$output" ;;
  *.tar.gz) cp "$HUMANSH_FAKE_ARCHIVE" "$output" ;;
  *) exit 22 ;;
esac
`
	if err := os.WriteFile(filepath.Join(fakeBin, "curl"), []byte(fakeCurl), 0o755); err != nil {
		t.Fatal(err)
	}

	env := isolatedEnvironment(home)
	env = removeEnvironmentKey(env, "HUMANSH_REPOSITORY")
	env = replaceEnvironmentValue(env, "PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	env = append(env, "HUMANSH_FAKE_ARCHIVE="+archivePath, "HUMANSH_FAKE_CHECKSUM="+checksumPath, "HUMANSH_FAKE_CURL_LOG="+curlLog)
	command := exec.Command("bash", filepath.Join(repo, "scripts", "install.sh"))
	command.Dir = repo
	command.Env = env
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("release install: %v\n%s", err, output)
	}

	installed := filepath.Join(home, ".local", "bin", "humansh")
	data, err := os.ReadFile(installed)
	if err != nil || !strings.Contains(string(data), "release-fixture") {
		t.Fatalf("release binary was not installed: err=%v data=%q", err, data)
	}
	requests, err := os.ReadFile(curlLog)
	if err != nil {
		t.Fatal(err)
	}
	base := "https://github.com/agenticlab-ai/humansh/releases/latest/download/"
	for _, want := range []string{base + asset, base + asset + ".sha256"} {
		if !strings.Contains(string(requests), want) {
			t.Errorf("release installer did not request %q:\n%s", want, requests)
		}
	}
	result := "Installation details\n\n" +
		"  Binary     ~/.local/bin/humansh\n" +
		"  License    MIT"
	text := string(output)
	completionIndex := strings.Index(text, "Installation details\n")
	if completionIndex < 0 {
		t.Fatalf("release install did not print a completion section:\n%s", output)
	}
	completion := text[completionIndex:]
	if !strings.Contains(completion, result) {
		t.Fatalf("release install did not print the aligned completion result:\n%s", output)
	}
	if strings.Contains(completion, installed) || strings.Contains(completion, "blob/main/LICENSE") || strings.Contains(completion, "provided \"as is\"") {
		t.Fatalf("release install printed an old lengthy completion value:\n%s", output)
	}
}

func writeReleaseFixture(t *testing.T, directory, asset string) (string, string) {
	t.Helper()
	archivePath := filepath.Join(directory, asset)
	archiveFile, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(archiveFile)
	tarWriter := tar.NewWriter(gzipWriter)
	payload := []byte("#!/bin/sh\necho release-fixture\n")
	if err := tarWriter.WriteHeader(&tar.Header{Name: "humansh", Mode: 0o755, Size: int64(len(payload)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archiveFile.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	checksumPath := archivePath + ".sha256"
	if err := os.WriteFile(checksumPath, []byte(fmt.Sprintf("%x  %s\n", digest, asset)), 0o600); err != nil {
		t.Fatal(err)
	}
	return archivePath, checksumPath
}

func TestLocalInstallSetupAndUninstall(t *testing.T) {
	repo := repositoryRoot(t)
	home := t.TempDir()
	env := isolatedEnvironment(home)
	env = readyCodexEnvironment(t, home, env)
	dotfiles := filepath.Join(home, "dotfiles")
	if err := os.Mkdir(dotfiles, 0o700); err != nil {
		t.Fatal(err)
	}
	startupTarget := filepath.Join(dotfiles, "zshrc")
	if err := os.WriteFile(startupTarget, []byte("keep-user-setting\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("dotfiles", "zshrc"), filepath.Join(home, ".zshrc")); err != nil {
		t.Fatal(err)
	}
	install := exec.Command("sh", filepath.Join(repo, "scripts", "install.sh"), "--local", "--shell", "bash")
	install.Dir = repo
	install.Env = env
	var output bytes.Buffer
	install.Stdout, install.Stderr = &output, &output
	if err := install.Run(); err != nil {
		t.Fatalf("install: %v\n%s", err, output.String())
	}
	if !strings.Contains(output.String(), "setup --shell bash") {
		t.Fatalf("non-interactive installer lost the Bash selection:\n%s", output.String())
	}
	binary := filepath.Join(home, ".local", "bin", "humansh")
	if info, err := os.Stat(binary); err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("installed binary: info=%v err=%v", info, err)
	}

	setup := exec.Command(binary, "setup", "--advanced", "--yes")
	setup.Env = env
	setup.Stdin = strings.NewReader("")
	output.Reset()
	setup.Stdout, setup.Stderr = &output, &output
	if err := setup.Run(); err != nil {
		t.Fatalf("setup: %v\n%s", err, output.String())
	}
	for _, want := range []string{"Shell activation patch", "--- ~/.zshrc (before)", "+++ ~/.zshrc (after)", "+# >>> humansh >>>", "+source ", "dotfiles/zshrc"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("installed setup review missing %q:\n%s", want, output.String())
		}
	}
	if strings.Contains(output.String(), "keep-user-setting") {
		t.Fatalf("installed setup review exposed unrelated .zshrc content:\n%s", output.String())
	}
	zshrc := filepath.Join(home, ".zshrc")
	data, err := os.ReadFile(zshrc)
	if err != nil || strings.Count(string(data), "# >>> humansh >>>") != 1 {
		t.Fatalf("managed startup: %v\n%s", err, data)
	}
	configPath := filepath.Join(home, "config", "humansh", "config.toml")
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("config not created: %v", err)
	}

	decline := exec.Command(binary, "uninstall", "--purge")
	decline.Env = env
	decline.Stdin = strings.NewReader("\n")
	output.Reset()
	decline.Stdout, decline.Stderr = &output, &output
	if err := decline.Run(); err != nil || !strings.Contains(output.String(), "Uninstall cancelled. Nothing was changed.") {
		t.Fatalf("declined installed-binary purge: %v\n%s", err, output.String())
	}
	for _, preserved := range []string{binary, configPath, zshrc} {
		if _, err := os.Lstat(preserved); err != nil {
			t.Fatalf("declined installed-binary purge removed %s: %v", preserved, err)
		}
	}
	data, err = os.ReadFile(zshrc)
	if err != nil || strings.Count(string(data), "# >>> humansh >>>") != 1 {
		t.Fatalf("declined installed-binary purge changed startup: %v\n%s", err, data)
	}

	// Exercise self-removal through the installed CLI. Unix keeps the running
	// executable mapped while humansh deletes its installed path last.
	uninstall := exec.Command(binary, "uninstall")
	uninstall.Env = env
	uninstall.Stdin = strings.NewReader("")
	output.Reset()
	uninstall.Stdout, uninstall.Stderr = &output, &output
	if err := uninstall.Run(); err != nil {
		t.Fatalf("uninstall: %v\n%s", err, output.String())
	}
	if _, err := os.Stat(binary); !os.IsNotExist(err) {
		t.Fatalf("binary still exists: %v", err)
	}
	data, err = os.ReadFile(zshrc)
	if err != nil || strings.Contains(string(data), "humansh") || !strings.Contains(string(data), "keep-user-setting") {
		t.Fatalf("managed block remains: %v\n%s", err, data)
	}
	if info, err := os.Lstat(zshrc); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("startup symlink was replaced: info=%v err=%v", info, err)
	}
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("normal uninstall removed config: %v", err)
	}

	// Repeated uninstall is a no-op and must remain successful.
	uninstall = exec.Command("sh", filepath.Join(repo, "scripts", "uninstall.sh"))
	uninstall.Env = env
	if data, err := uninstall.CombinedOutput(); err != nil {
		t.Fatalf("second uninstall: %v\n%s", err, data)
	}
}

func TestLocalInstallerStopsCleanlyAndRestoresPreviousBinaryOnProviderFailure(t *testing.T) {
	repo := repositoryRoot(t)
	home := t.TempDir()
	installDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(installDir, "humansh")
	const previous = "#!/bin/sh\necho previous-humansh\n"
	if err := os.WriteFile(installed, []byte(previous), 0o755); err != nil {
		t.Fatal(err)
	}
	fixtures := filepath.Join(home, "fixtures")
	if err := os.Mkdir(fixtures, 0o700); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(fixtures, "replacement")
	if err := os.WriteFile(replacement, []byte("#!/bin/sh\nif [ \"${1-}\" = setup ]; then echo simulated-provider-proof-failure >&2; exit 21; fi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	fakeBin := filepath.Join(home, "fake-bin")
	if err := os.Mkdir(fakeBin, 0o700); err != nil {
		t.Fatal(err)
	}
	fakeGo := "#!/bin/sh\nwhile [ \"$#\" -gt 0 ]; do\n  if [ \"$1\" = -o ]; then cp \"$HUMANSH_FAKE_REPLACEMENT\" \"$2\"; exit 0; fi\n  shift\ndone\nexit 2\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "go"), []byte(fakeGo), 0o755); err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(fixtures, "run-installer")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nsh \"$HUMANSH_INSTALLER\" --local\nresult=$?\necho INSTALL_STATUS:$result\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := isolatedEnvironment(home)
	env = removeEnvironmentKey(env, "HUMANSH_NONINTERACTIVE")
	env = replaceEnvironmentValue(env, "PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	env = append(env, "HUMANSH_FAKE_REPLACEMENT="+replacement, "HUMANSH_INSTALLER="+filepath.Join(repo, "scripts", "install.sh"))
	ptyScript := `zmodload zsh/zpty || exit 90
zpty -b I "$HUMANSH_INSTALL_WRAPPER"
seen=''
for attempt in {1..500}; do
  while zpty -r -t I chunk; do seen+=$chunk; done
  [[ $seen == *INSTALL_STATUS:0* ]] && { print -r -- "$seen"; zpty -d I; exit 0; }
  sleep 0.02
done
print -ru2 -- "installer did not finish: ${(V)seen}"
zpty -d I
exit 91`
	command := exec.Command("zsh", "-f", "-c", ptyScript)
	command.Dir = repo
	command.Env = append(env, "HUMANSH_INSTALL_WRAPPER="+wrapper)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("failed installer PTY: %v\n%s", err, output)
	}
	text := string(output)
	for _, want := range []string{"simulated-provider-proof-failure", "Installation stopped", "Fix the provider issue above, then run the installer again.", "INSTALL_STATUS:0"} {
		if !strings.Contains(text, want) {
			t.Errorf("stopped installer output missing %q:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"Installation details", "Incomplete", "INSTALL_STATUS:21", "rolling back"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("stopped installer output included %q:\n%s", unwanted, text)
		}
	}
	data, err := os.ReadFile(installed)
	if err != nil || string(data) != previous {
		t.Fatalf("previous binary was not restored: err=%v data=%q", err, data)
	}
	leftovers, err := filepath.Glob(filepath.Join(installDir, ".humansh-*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("installer left temporary binaries: %v err=%v", leftovers, err)
	}
}

func TestPipedInteractiveInstallerRestoresBinaryRemovedDuringSetupBeforeCompletion(t *testing.T) {
	repo := repositoryRoot(t)
	home := t.TempDir()
	fixtures := filepath.Join(home, "fixtures")
	if err := os.Mkdir(fixtures, 0o700); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(fixtures, "replacement")
	program := `#!/bin/sh
case ${1-} in
  setup) echo SETUP_COMPLETE; rm -f "$0" ;;
  *) exit 2 ;;
esac
`
	if err := os.WriteFile(replacement, []byte(program), 0o755); err != nil {
		t.Fatal(err)
	}
	fakeBin := filepath.Join(home, "fake-bin")
	if err := os.Mkdir(fakeBin, 0o700); err != nil {
		t.Fatal(err)
	}
	fakeGo := "#!/bin/sh\nwhile [ \"$#\" -gt 0 ]; do\n  if [ \"$1\" = -o ]; then cp \"$HUMANSH_FAKE_REPLACEMENT\" \"$2\"; exit 0; fi\n  shift\ndone\nexit 2\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "go"), []byte(fakeGo), 0o755); err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(fixtures, "run-installer")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nsh \"$HUMANSH_INSTALLER\" --local </dev/null\nresult=$?\necho INSTALL_STATUS:$result\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := isolatedEnvironment(home)
	env = removeEnvironmentKey(env, "HUMANSH_NONINTERACTIVE")
	env = replaceEnvironmentValue(env, "PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	env = append(env, "HUMANSH_FAKE_REPLACEMENT="+replacement, "HUMANSH_INSTALLER="+filepath.Join(repo, "scripts", "install.sh"))
	ptyScript := `zmodload zsh/zpty || exit 90
zpty -b I "$HUMANSH_INSTALL_WRAPPER"
seen=''
for attempt in {1..500}; do
  while zpty -r -t I chunk; do seen+=$chunk; done
  [[ $seen == *INSTALL_STATUS:0* ]] && { print -r -- "$seen"; zpty -d I; exit 0; }
  sleep 0.02
done
print -ru2 -- "installer did not finish: ${(V)seen}"
zpty -d I
exit 91`
	command := exec.Command("zsh", "-f", "-c", ptyScript)
	command.Dir = repo
	command.Env = append(env, "HUMANSH_INSTALL_WRAPPER="+wrapper)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("interactive installer PTY: %v\n%s", err, output)
	}
	text := string(output)
	setupIndex := strings.Index(text, "SETUP_COMPLETE")
	restoredIndex := strings.Index(text, "installed binary disappeared during setup and was restored")
	installedIndex := strings.Index(text, "Installation details")
	if setupIndex < 0 || restoredIndex <= setupIndex || installedIndex <= restoredIndex {
		t.Fatalf("installer did not restore the binary before completing setup:\n%s", text)
	}
	if strings.Contains(text, "ONBOARDING_STARTED") || strings.Contains(text, "No such file or directory") || strings.Contains(text, "onboarding could not be shown") {
		t.Fatalf("installer ran the removed onboarding step or reproduced the missing-binary failure:\n%s", text)
	}
	installed := filepath.Join(home, ".local", "bin", "humansh")
	if info, err := os.Stat(installed); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("restored installed binary: info=%v err=%v\n%s", info, err, text)
	}
}

func TestUninstallFailsClosedOnCorruptMarkers(t *testing.T) {
	repo := repositoryRoot(t)
	home := t.TempDir()
	env := isolatedEnvironment(home)
	startup := filepath.Join(home, ".zshrc")
	if err := os.WriteFile(startup, []byte("keep\n# >>> humansh >>>\nkeep-after\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(home, ".local", "bin", "humansh")
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("sentinel"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", filepath.Join(repo, "scripts", "uninstall.sh"))
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "markers are corrupted") {
		t.Fatalf("error=%v output=%s", err, output)
	}
	if data, _ := os.ReadFile(startup); string(data) != "keep\n# >>> humansh >>>\nkeep-after\n" {
		t.Fatalf("startup changed: %q", data)
	}
	if _, err := os.Stat(binary); err != nil {
		t.Fatalf("binary removed after failed preflight: %v", err)
	}
}

func TestUninstallFailsClosedOnReversedMarkers(t *testing.T) {
	repo := repositoryRoot(t)
	home := t.TempDir()
	env := isolatedEnvironment(home)
	startup := filepath.Join(home, ".zshrc")
	original := "keep-before\n# <<< humansh <<<\nkeep-middle\n# >>> humansh >>>\nkeep-after\n"
	if err := os.WriteFile(startup, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(home, ".local", "bin", "humansh")
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("sentinel"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", filepath.Join(repo, "scripts", "uninstall.sh"))
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "markers are corrupted") {
		t.Fatalf("error=%v output=%s", err, output)
	}
	if data, readErr := os.ReadFile(startup); readErr != nil || string(data) != original {
		t.Fatalf("startup changed: %v %q", readErr, data)
	}
	if _, err := os.Stat(binary); err != nil {
		t.Fatalf("binary removed after failed preflight: %v", err)
	}
}

func TestUninstallRejectsInstallStatePathRedirection(t *testing.T) {
	repo := repositoryRoot(t)
	home := t.TempDir()
	env := isolatedEnvironment(home)
	binary := filepath.Join(home, ".local", "bin", "humansh")
	unrelated := filepath.Join(home, "unrelated")
	for _, path := range []string{binary, unrelated} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("sentinel"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	dataDir := filepath.Join(home, "data", "humansh")
	state := filepath.Join(dataDir, "install-state.toml")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stateData := "version = 1\n" +
		"binary_path = \"" + unrelated + "\"\n" +
		"installed_version = \"test\"\n" +
		"shell = \"zsh\"\n" +
		"protocol = \"zle-v1\"\n" +
		"shell_asset_path = \"" + filepath.Join(dataDir, "shell", "zsh", "humansh.zsh") + "\"\n" +
		"shell_asset_sha256 = \"" + strings.Repeat("0", 64) + "\"\n" +
		"startup_file = \"" + filepath.Join(home, ".zshrc") + "\"\n" +
		"managed_block_version = 1\n"
	if err := os.WriteFile(state, []byte(stateData), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", filepath.Join(repo, "scripts", "uninstall.sh"))
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "paths do not match") {
		t.Fatalf("error=%v output=%s", err, output)
	}
	for _, path := range []string{binary, unrelated, state} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("failed preflight removed %s: %v", path, err)
		}
	}
}

func TestUninstallRejectsSymlinkedInstallStateBeforeChanges(t *testing.T) {
	repo := repositoryRoot(t)
	home := t.TempDir()
	env := isolatedEnvironment(home)
	binary := filepath.Join(home, ".local", "bin", "humansh")
	if err := os.MkdirAll(filepath.Dir(binary), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("sentinel"), 0o755); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(home, "data", "humansh")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, "unrelated-state")
	if err := os.WriteFile(target, []byte("unrelated"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dataDir, "install-state.toml")
	if err := os.Symlink(target, state); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("sh", filepath.Join(repo, "scripts", "uninstall.sh"))
	command.Env = env
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "not a symlink") {
		t.Fatalf("error=%v output=%s", err, output)
	}
	for _, path := range []string{binary, target, state} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("failed preflight removed %s: %v", path, err)
		}
	}
}

func TestStandaloneUninstallUsesBashInstallState(t *testing.T) {
	repo := repositoryRoot(t)
	home := t.TempDir()
	env := isolatedEnvironment(home)
	fakeBin := filepath.Join(home, "fake-bin")
	if err := os.Mkdir(fakeBin, 0o700); err != nil {
		t.Fatal(err)
	}
	// GNU stat uses -f for filesystem information and can emit output before
	// rejecting the BSD format operand. Ensure that failed output cannot leak
	// into the fallback mode value used by the portable uninstaller.
	fakeStat := `#!/bin/sh
case ${1-} in
  -f) echo leaked-filesystem-output; exit 1 ;;
  -c)
    case ${3-} in
      *install-state.toml) echo 600 ;;
      *) echo 640 ;;
    esac
    exit 0 ;;
esac
exit 2
`
	if err := os.WriteFile(filepath.Join(fakeBin, "stat"), []byte(fakeStat), 0o755); err != nil {
		t.Fatal(err)
	}
	env = replaceEnvironmentValue(env, "PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	binary := filepath.Join(home, ".local", "bin", "humansh")
	dataDir := filepath.Join(home, "data", "humansh")
	asset := filepath.Join(dataDir, "shell", "bash", "humansh.bash")
	startup := filepath.Join(home, ".bashrc")
	state := filepath.Join(dataDir, "install-state.toml")
	for _, path := range []string{binary, asset} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("managed"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	startupData := "keep-before\n# >>> humansh >>>\nsource managed\n# <<< humansh <<<\nkeep-after\n"
	if err := os.WriteFile(startup, []byte(startupData), 0o640); err != nil {
		t.Fatal(err)
	}
	stateData := "version = 1\n" +
		"binary_path = \"" + binary + "\"\n" +
		"installed_version = \"test\"\n" +
		"shell = \"bash\"\n" +
		"protocol = \"readline-v1\"\n" +
		"shell_asset_path = \"" + asset + "\"\n" +
		"shell_asset_sha256 = \"" + strings.Repeat("0", 64) + "\"\n" +
		"startup_file = \"" + startup + "\"\n" +
		"managed_block_version = 1\n"
	if err := os.WriteFile(state, []byte(stateData), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("sh", filepath.Join(repo, "scripts", "uninstall.sh"))
	command.Env = env
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Bash standalone uninstall: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "restart that shell") || !strings.Contains(string(output), "configuration and credentials were preserved") {
		t.Fatalf("Bash standalone uninstall output:\n%s", output)
	}
	for _, path := range []string{binary, asset, state} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("Bash standalone uninstall retained %s: %v", path, err)
		}
	}
	if data, err := os.ReadFile(startup); err != nil || strings.Contains(string(data), "humansh") || !strings.Contains(string(data), "keep-before") || !strings.Contains(string(data), "keep-after") {
		t.Fatalf("Bash startup after uninstall=%q err=%v", data, err)
	}
}

func TestStandaloneUninstallRemovesAllVersionTwoShellIntegrations(t *testing.T) {
	repo := repositoryRoot(t)
	home := t.TempDir()
	env := isolatedEnvironment(home)
	binary := filepath.Join(home, ".local", "bin", "humansh")
	dataDir := filepath.Join(home, "data", "humansh")
	state := filepath.Join(dataDir, "install-state.toml")
	zshAsset := filepath.Join(dataDir, "shell", "zsh", "humansh.zsh")
	bashAsset := filepath.Join(dataDir, "shell", "bash", "humansh.bash")
	zshStartup := filepath.Join(home, ".zshrc")
	bashStartup := filepath.Join(home, ".bashrc")
	for _, path := range []string{binary, zshAsset, bashAsset} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("managed"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	startupData := "keep-before\n# >>> humansh >>>\nsource managed\n# <<< humansh <<<\nkeep-after\n"
	for _, startup := range []string{zshStartup, bashStartup} {
		if err := os.WriteFile(startup, []byte(startupData), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	digest := strings.Repeat("0", 64)
	stateData := "version = 2\n" +
		"binary_path = \"" + binary + "\"\n" +
		"installed_version = \"test\"\n" +
		"shells = \"zsh,bash\"\n" +
		"zsh_protocol = \"zle-v1\"\n" +
		"zsh_shell_asset_path = \"" + zshAsset + "\"\n" +
		"zsh_shell_asset_sha256 = \"" + digest + "\"\n" +
		"zsh_startup_file = \"" + zshStartup + "\"\n" +
		"bash_protocol = \"readline-v1\"\n" +
		"bash_shell_asset_path = \"" + bashAsset + "\"\n" +
		"bash_shell_asset_sha256 = \"" + digest + "\"\n" +
		"bash_startup_file = \"" + bashStartup + "\"\n" +
		"managed_block_version = 1\n"
	if err := os.WriteFile(state, []byte(stateData), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("sh", filepath.Join(repo, "scripts", "uninstall.sh"))
	command.Env = env
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("multi-shell standalone uninstall: %v\n%s", err, output)
	}
	for _, path := range []string{binary, zshAsset, bashAsset, state} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("multi-shell uninstall retained %s: %v", path, err)
		}
	}
	for _, startup := range []string{zshStartup, bashStartup} {
		if data, err := os.ReadFile(startup); err != nil || strings.Contains(string(data), "humansh") || !strings.Contains(string(data), "keep-before") || !strings.Contains(string(data), "keep-after") {
			t.Errorf("startup %s after uninstall=%q err=%v", startup, data, err)
		}
	}
}

func TestPurgeRemovesOnlyHumanshDirectories(t *testing.T) {
	repo := repositoryRoot(t)
	home := t.TempDir()
	env := isolatedEnvironment(home)
	if runtime.GOOS == "darwin" {
		fakeBin := filepath.Join(home, "fake-bin")
		if err := os.Mkdir(fakeBin, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fakeBin, "security"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		env = replaceEnvironmentValue(env, "PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	configDir := filepath.Join(home, "config", "humansh")
	dataDir := filepath.Join(home, "data", "humansh")
	unrelated := filepath.Join(home, "config", "other", "keep")
	for _, path := range []string{filepath.Join(configDir, "config.toml"), filepath.Join(dataDir, "owned-cache"), unrelated} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("sh", filepath.Join(repo, "scripts", "uninstall.sh"), "--purge")
	cmd.Env = env
	cmd.Stdin = strings.NewReader("yes\n")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("purge: %v\n%s", err, output)
	}
	for _, path := range []string{configDir, dataDir} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("purge retained %s: %v", path, err)
		}
	}
	if data, err := os.ReadFile(unrelated); err != nil || string(data) != "keep" {
		t.Fatalf("purge damaged unrelated file: %v %q", err, data)
	}
}

func TestStandalonePurgeDeclineCancelsBeforeAnyRemoval(t *testing.T) {
	repo := repositoryRoot(t)
	home := t.TempDir()
	env := isolatedEnvironment(home)
	startup := filepath.Join(home, ".zshrc")
	binary := filepath.Join(home, ".local", "bin", "humansh")
	configFile := filepath.Join(home, "config", "humansh", "config.toml")
	originalStartup := "keep\n# >>> humansh >>>\nsource managed\n# <<< humansh <<<\nkeep-after\n"
	for path, data := range map[string]string{startup: originalStartup, binary: "binary", configFile: "config"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o600)
		if path == binary {
			mode = 0o755
		}
		if err := os.WriteFile(path, []byte(data), mode); err != nil {
			t.Fatal(err)
		}
	}
	command := exec.Command("sh", filepath.Join(repo, "scripts", "uninstall.sh"), "--purge")
	command.Env = env
	command.Stdin = strings.NewReader("\n")
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "Uninstall cancelled. Nothing was changed.") || strings.Contains(string(output), "uninstalled;") {
		t.Fatalf("declined purge err=%v output=%s", err, output)
	}
	for path, want := range map[string]string{startup: originalStartup, binary: "binary", configFile: "config"} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Fatalf("declined purge changed %s: err=%v data=%q", path, err, data)
		}
	}
}

func replaceEnvironmentValue(environment []string, key, value string) []string {
	prefix := key + "="
	for index, item := range environment {
		if strings.HasPrefix(item, prefix) {
			environment[index] = prefix + value
			return environment
		}
	}
	return append(environment, prefix+value)
}

func removeEnvironmentKey(environment []string, key string) []string {
	prefix := key + "="
	out := environment[:0]
	for _, item := range environment {
		if !strings.HasPrefix(item, prefix) {
			out = append(out, item)
		}
	}
	return out
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func isolatedEnvironment(home string) []string {
	cacheBase := filepath.Join(os.TempDir(), "humansh-installer-test-cache")
	values := []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, "config"),
		"XDG_DATA_HOME=" + filepath.Join(home, "data"),
		"XDG_CACHE_HOME=" + filepath.Join(home, "cache"),
		"CODEX_HOME=" + filepath.Join(home, "codex"),
		"OPENROUTER_API_KEY=test-only-installer-key",
		"HUMANSH_NONINTERACTIVE=1",
		"GOCACHE=" + filepath.Join(cacheBase, "build"),
		"GOMODCACHE=" + filepath.Join(cacheBase, "modules"),
		"GOPATH=" + filepath.Join(cacheBase, "gopath"),
	}
	for _, key := range []string{"PATH", "TMPDIR"} {
		if value := os.Getenv(key); value != "" {
			values = append(values, key+"="+value)
		}
	}
	return values
}

func readyCodexEnvironment(t *testing.T, home string, environment []string) []string {
	t.Helper()
	binDir := filepath.Join(home, "ready-provider-bin")
	if err := os.Mkdir(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
case "${1-} ${2-}" in
  "exec Reply with exactly HUMANSH_READY and nothing else. Do not use tools or inspect external state.") echo "HUMANSH_READY" ;;
  *) exit 2 ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "codex"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	path := os.Getenv("PATH")
	for _, item := range environment {
		if strings.HasPrefix(item, "PATH=") {
			path = strings.TrimPrefix(item, "PATH=")
			break
		}
	}
	return replaceEnvironmentValue(environment, "PATH", binDir+string(os.PathListSeparator)+path)
}
