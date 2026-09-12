package commandgrammar

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

var (
	helpFixtureBinary      []byte
	helpFixtureReplacement []byte
)

func TestMain(m *testing.M) {
	directory, err := os.MkdirTemp("", "humansh-help-fixture-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	path := filepath.Join(directory, "fixturevcs")
	command := exec.Command("go", "build", "-o", path, "./testdata/helpfixture")
	if output, buildErr := command.CombinedOutput(); buildErr != nil {
		fmt.Fprintf(os.Stderr, "build command-help fixture: %v\n%s", buildErr, output)
		_ = os.RemoveAll(directory)
		os.Exit(1)
	}
	helpFixtureBinary, err = os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		_ = os.RemoveAll(directory)
		os.Exit(1)
	}
	replacementPath := filepath.Join(directory, "fixturevcs-replacement")
	replacementBuild := exec.Command("go", "build", "-ldflags=-X main.extraCommand=inspect", "-o", replacementPath, "./testdata/helpfixture")
	if output, buildErr := replacementBuild.CombinedOutput(); buildErr != nil {
		fmt.Fprintf(os.Stderr, "build replacement command-help fixture: %v\n%s", buildErr, output)
		_ = os.RemoveAll(directory)
		os.Exit(1)
	}
	helpFixtureReplacement, err = os.ReadFile(replacementPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		_ = os.RemoveAll(directory)
		os.Exit(1)
	}
	_ = os.RemoveAll(directory)
	os.Exit(m.Run())
}

func TestRuntimeHelpSourceUsesExactExecutableAndFixedHelpArguments(t *testing.T) {
	path := buildHelpFixture(t, "fixturevcs")
	t.Setenv("HUMANSH_SECRET_MARKER", "must-not-reach-help")
	analyzer := testRuntimeAnalyzer(RuntimeHelpSource{Timeout: 5 * time.Second})

	inv := invocation("fixturevcs is failing please authenticate")
	inv.ExecutablePath = path
	analysis := analyzer.Analyze(context.Background(), inv)
	if analysis.Source != "installed_help" || analysis.StopReason != StopUndocumentedSubcommand || analysis.Boundary != 1 {
		t.Fatalf("analysis=%+v", analysis)
	}
	logData, err := os.ReadFile(path + ".log")
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logData)
	if !strings.Contains(logText, "args=--help") || !strings.Contains(logText, "stdin=0") || strings.Contains(logText, " is ") || strings.Contains(logText, "secret=true") || !strings.Contains(logText, "locale=C") {
		t.Fatalf("unsafe or unexpected help invocation: %s", logText)
	}
	for _, field := range strings.Fields(strings.Split(logText, "\n")[0]) {
		if directory, ok := strings.CutPrefix(field, "cwd="); ok {
			if _, statErr := os.Stat(directory); !os.IsNotExist(statErr) {
				t.Fatalf("isolated help directory was not removed: %s (%v)", directory, statErr)
			}
		}
	}

	commit := invocation(`fixturevcs commit -am "please_authenticate"`)
	commit.ExecutablePath = path
	commitAnalysis := analyzer.Analyze(context.Background(), commit)
	if commitAnalysis.StopReason != StopComplete || commitAnalysis.RoleAt(2) != RoleOption || commitAnalysis.RoleAt(3) != RoleOptionValue {
		t.Fatalf("commit analysis=%+v annotations=%+v", commitAnalysis, commitAnalysis.Annotations)
	}
	logData, _ = os.ReadFile(path + ".log")
	if strings.Contains(string(logData), "please_authenticate") || !strings.Contains(string(logData), "args=commit --help") {
		t.Fatalf("typed option value reached help process: %s", logData)
	}
}

func TestRuntimeHelpSourceResolvesPATHAndObservesChangedHelp(t *testing.T) {
	path := buildHelpFixture(t, "fixturevcs")
	t.Setenv("PATH", filepath.Dir(path)+string(os.PathListSeparator)+os.Getenv("PATH"))
	analyzer := testRuntimeAnalyzer(RuntimeHelpSource{Timeout: 5 * time.Second})

	first := analyzer.Analyze(context.Background(), invocation("fixturevcs inspect"))
	if first.StopReason != StopUndocumentedSubcommand {
		t.Fatalf("first analysis=%+v", first)
	}
	replacementPath := path + ".next"
	if err := os.WriteFile(replacementPath, helpFixtureReplacement, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacementPath, path); err != nil {
		t.Fatal(err)
	}
	second := analyzer.Analyze(context.Background(), invocation("fixturevcs inspect"))
	if second.StopReason != StopComplete || second.RoleAt(1) != RoleSubcommand {
		t.Fatalf("changed help was not observed: %+v", second)
	}
}

func TestRuntimeHelpSourceAcceptsStderrAndNonzeroHelpExit(t *testing.T) {
	original := buildHelpFixture(t, "fixturevcs")
	path := copyExecutable(t, original, "fixturestderr")
	inv := invocation("fixturestderr status")
	inv.ExecutablePath = path
	analysis := testRuntimeAnalyzer(RuntimeHelpSource{Timeout: 5 * time.Second}).Analyze(context.Background(), inv)
	if !analysis.Modeled() || analysis.StopReason != StopComplete || analysis.RoleAt(1) != RoleSubcommand {
		t.Fatalf("stderr/nonzero analysis=%+v", analysis)
	}
}

func TestRuntimeHelpSourceParsesBSDCompactFlagsWithoutForwardingTypedArguments(t *testing.T) {
	original := buildHelpFixture(t, "fixturevcs")
	path := copyExecutable(t, original, "fixturebsdusage")
	inv := invocation("fixturebsdusage -rf internal/commandgrammar/")
	inv.ExecutablePath = path
	analysis := testRuntimeAnalyzer(RuntimeHelpSource{Timeout: 5 * time.Second}).Analyze(context.Background(), inv)
	if analysis.Coverage != CoverageRecognized || analysis.StopReason != StopComplete || analysis.Boundary != 3 || analysis.RoleAt(1) != RoleOption || analysis.RoleAt(2) != RolePositional {
		t.Fatalf("BSD compact-option analysis=%+v annotations=%+v", analysis, analysis.Annotations)
	}
	logData, err := os.ReadFile(path + ".log")
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logData)
	if !strings.Contains(logText, "args=--help") || strings.Contains(logText, "-rf") || strings.Contains(logText, "internal/commandgrammar") {
		t.Fatalf("typed BSD invocation reached help process: %s", logText)
	}
}

func TestRuntimeHelpSourceParsesInstalledBSDRMCompactFlags(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin /bin/rm help syntax regression")
	}
	const path = "/bin/rm"
	if _, err := os.Stat(path); err != nil {
		t.Skipf("%s is unavailable: %v", path, err)
	}
	inv := invocation("rm -rf internal/commandgrammar/")
	inv.ExecutablePath = path
	analysis := testRuntimeAnalyzer(RuntimeHelpSource{Timeout: 5 * time.Second}).Analyze(context.Background(), inv)
	if analysis.Coverage != CoverageRecognized || analysis.StopReason != StopComplete || analysis.Boundary != 3 || analysis.RoleAt(1) != RoleOption || analysis.RoleAt(2) != RolePositional {
		t.Fatalf("installed BSD rm compact-option analysis=%+v annotations=%+v", analysis, analysis.Annotations)
	}
}

func TestRuntimeHelpSourcePreservesInstalledDockerCommandTail(t *testing.T) {
	path, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("Docker CLI is not installed; deterministic installed-shell coverage runs separately")
	}
	inv := invocation("docker run --rm --network host docker.io/library/node@sha256:6dac556d980b7f0e5498d08f08cee0ca67798b4ad6c23964a9214920e67758d0 curl --silent --show-error --fail --max-time 8 http://127.0.0.1:3000/api/v1/version")
	inv.ExecutablePath = path
	analysis := testRuntimeAnalyzer(RuntimeHelpSource{Timeout: 5 * time.Second}).Analyze(context.Background(), inv)
	if analysis.Coverage != CoveragePartial || analysis.StopReason != StopComplete || analysis.Boundary != 5 || analysis.RoleAt(4) != RoleOptionValue || analysis.RoleAt(7) != RolePositional {
		t.Fatalf("installed Docker command tail analysis=%+v annotations=%+v", analysis, analysis.Annotations)
	}
}

func TestRuntimeHelpSourceTimesOutAndFallsBack(t *testing.T) {
	original := buildHelpFixture(t, "fixturevcs")
	path := copyExecutable(t, original, "fixturehang")
	inv := invocation("fixturehang status")
	inv.ExecutablePath = path
	started := time.Now()
	analysis := testRuntimeAnalyzer(RuntimeHelpSource{Timeout: 60 * time.Millisecond}).Analyze(context.Background(), inv)
	if analysis.Modeled() || time.Since(started) > time.Second {
		t.Fatalf("timeout analysis=%+v elapsed=%s", analysis, time.Since(started))
	}
}

func TestRuntimeHelpSourceBoundsAndMarksLargeOutputIncomplete(t *testing.T) {
	original := buildHelpFixture(t, "fixturevcs")
	path := copyExecutable(t, original, "fixturehuge")
	inv := invocation("fixturehuge status")
	inv.ExecutablePath = path
	analysis := testRuntimeAnalyzer(RuntimeHelpSource{Timeout: 5 * time.Second, MaxOutput: 1024}).Analyze(context.Background(), inv)
	if analysis.Coverage != CoveragePartial || analysis.StopReason != StopComplete || analysis.RoleAt(1) != RoleSubcommand {
		t.Fatalf("large-output analysis=%+v", analysis)
	}
}

func TestRuntimeHelpSourceUsesExactShellResolvedPathWithDifferentBasename(t *testing.T) {
	path := buildHelpFixture(t, "fixturevcs")
	inv := invocation("different status")
	inv.ExecutablePath = path
	analysis := testRuntimeAnalyzer(RuntimeHelpSource{Timeout: 5 * time.Second}).Analyze(context.Background(), inv)
	if !analysis.Modeled() || analysis.StopReason != StopComplete || analysis.RoleAt(1) != RoleSubcommand {
		t.Fatalf("exact shell-resolved path was not used: %+v", analysis)
	}
	if _, err := os.Stat(path + ".log"); err != nil {
		t.Fatalf("shell-resolved executable was not invoked: %v", err)
	}
}

func buildHelpFixture(t *testing.T, name string) string {
	t.Helper()
	extension := ""
	if runtime.GOOS == "windows" {
		extension = ".exe"
	}
	path := filepath.Join(t.TempDir(), name+extension)
	if err := os.WriteFile(path, helpFixtureBinary, 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func testRuntimeAnalyzer(source RuntimeHelpSource) *HelpAnalyzer {
	analyzer := NewAnalyzer(source)
	analyzer.timeout = 15 * time.Second
	return analyzer
}

func copyExecutable(t *testing.T, source, name string) string {
	t.Helper()
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(destination, data, 0700); err != nil {
		t.Fatal(err)
	}
	return destination
}
