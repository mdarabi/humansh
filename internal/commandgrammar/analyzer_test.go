package commandgrammar

import (
	"context"
	"strings"
	"testing"
)

func TestHelpAnalyzerTraversesGenericInstalledGrammar(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		input     string
		coverage  Coverage
		stop      StopReason
		boundary  int
		roles     []Role
		probes    []string
		uncertain bool
	}{
		{
			name: "known-subcommand", input: "fixturevcs status", coverage: CoverageRecognized, stop: StopComplete, boundary: 2,
			roles: []Role{RoleHead, RoleSubcommand}, probes: []string{""},
		},
		{
			name: "global-and-leaf-options", input: "fixturevcs --no-pager status --short", coverage: CoverageRecognized, stop: StopComplete, boundary: 4,
			roles: []Role{RoleHead, RoleOption, RoleSubcommand, RoleOption}, probes: []string{"", "status"},
		},
		{
			name: "separate-value", input: "fixturevcs -C repo status", coverage: CoverageRecognized, stop: StopComplete, boundary: 4,
			roles: []Role{RoleHead, RoleOption, RoleOptionValue, RoleSubcommand}, probes: []string{""},
		},
		{
			name: "attached-only-value", input: "fixturevcs --config=repo status", coverage: CoverageRecognized, stop: StopComplete, boundary: 3,
			roles: []Role{RoleHead, RoleOption, RoleSubcommand}, probes: []string{""},
		},
		{
			name: "attached-only-value-rejects-separate-form", input: "fixturevcs --config repo status", coverage: CoverageIndeterminate, stop: StopMissingOptionValue, boundary: 2, uncertain: true,
			roles: []Role{RoleHead, RoleOption, RoleUnexpected, RoleUnexpected}, probes: []string{""},
		},
		{
			name: "opaque-option-value", input: `fixturevcs commit -m "please_authenticate"`, coverage: CoverageRecognized, stop: StopComplete, boundary: 4,
			roles: []Role{RoleHead, RoleSubcommand, RoleOption, RoleOptionValue}, probes: []string{"", "commit"},
		},
		{
			name: "short-cluster-ending-in-separate-value", input: "fixturevcs commit -am message", coverage: CoverageRecognized, stop: StopComplete, boundary: 4,
			roles: []Role{RoleHead, RoleSubcommand, RoleOption, RoleOptionValue}, probes: []string{"", "commit"},
		},
		{
			name: "short-cluster-with-attached-value", input: "fixturevcs commit -ammessage", coverage: CoverageRecognized, stop: StopComplete, boundary: 3,
			roles: []Role{RoleHead, RoleSubcommand, RoleOption}, probes: []string{"", "commit"},
		},
		{
			name: "required-value-does-not-swallow-next-option", input: "fixturevcs commit -m --help", coverage: CoverageIndeterminate, stop: StopMissingOptionValue, boundary: 3, uncertain: true,
			roles: []Role{RoleHead, RoleSubcommand, RoleOption, RoleUnexpected}, probes: []string{"", "commit"},
		},
		{
			name: "terminal-option", input: "fixturevcs status --help is failing", coverage: CoverageRecognized, stop: StopComplete, boundary: 3,
			roles: []Role{RoleHead, RoleSubcommand, RoleOption, RoleUnexpected, RoleUnexpected}, probes: []string{"", "status"},
		},
		{
			name: "nested-command", input: "fixturevcs remote add origin upstream", coverage: CoverageRecognized, stop: StopComplete, boundary: 5,
			roles: []Role{RoleHead, RoleSubcommand, RoleSubcommand, RolePositional, RolePositional}, probes: []string{"", "remote", "remote add"},
		},
		{
			name: "double-dash", input: "fixturevcs status -- is failing", coverage: CoverageRecognized, stop: StopComplete, boundary: 5,
			roles: []Role{RoleHead, RoleSubcommand, RoleOption, RolePositional, RolePositional}, probes: []string{"", "status"},
		},
		{
			name: "undocumented-root-word", input: "fixturevcs is failing please authenticate", coverage: CoverageIndeterminate, stop: StopUndocumentedSubcommand, boundary: 1, uncertain: true,
			roles: []Role{RoleHead, RoleUnexpected, RoleUnexpected, RoleUnexpected, RoleUnexpected}, probes: []string{""},
		},
		{
			name: "unknown-leaf-option", input: "fixturevcs status --future", coverage: CoverageIndeterminate, stop: StopUnknownOption, boundary: 2, uncertain: true,
			roles: []Role{RoleHead, RoleSubcommand, RoleUnexpected}, probes: []string{"", "status"},
		},
		{
			name: "missing-global-option-value", input: "fixturevcs -C", coverage: CoverageIndeterminate, stop: StopMissingOptionValue, boundary: 2, uncertain: true,
			roles: []Role{RoleHead, RoleOption}, probes: []string{""},
		},
		{
			name: "unknown-nested-command", input: "fixturevcs remote is status", coverage: CoverageIndeterminate, stop: StopUndocumentedSubcommand, boundary: 2, uncertain: true,
			roles: []Role{RoleHead, RoleSubcommand, RoleUnexpected, RoleUnexpected}, probes: []string{"", "remote"},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			source := fixtureHelpSource()
			analysis := NewAnalyzer(source).Analyze(context.Background(), invocation(test.input))
			if analysis.Coverage != test.coverage || analysis.StopReason != test.stop || analysis.Boundary != test.boundary || analysis.Uncertain() != test.uncertain {
				t.Fatalf("Analyze(%q) = coverage=%s stop=%s boundary=%d uncertain=%t; want %s/%s/%d/%t", test.input, analysis.Coverage, analysis.StopReason, analysis.Boundary, analysis.Uncertain(), test.coverage, test.stop, test.boundary, test.uncertain)
			}
			if len(analysis.Annotations) != len(test.roles) {
				t.Fatalf("Analyze(%q) returned %d annotations, want %d", test.input, len(analysis.Annotations), len(test.roles))
			}
			for index, role := range test.roles {
				if got := analysis.RoleAt(index); got != role {
					t.Errorf("Analyze(%q) role[%d]=%s, want %s", test.input, index, got, role)
				}
			}
			if got := strings.Join(source.session.calls, ","); got != strings.Join(test.probes, ",") {
				t.Fatalf("help probes=%q, want %q", got, strings.Join(test.probes, ","))
			}
		})
	}
}

func TestHelpAnalyzerPreservesForwardedCommandBoundary(t *testing.T) {
	t.Parallel()
	root, err := ParseHelp([]byte("Usage: fixturebox COMMAND\n\nCommands:\n  run  Run a command\n"), true)
	if err != nil {
		t.Fatal(err)
	}
	run, err := ParseHelp([]byte(`Usage: fixturebox run [OPTIONS] TARGET [COMMAND] [ARG...]

Options:
  --network network  Connect to a network
  --rm               Remove after exit
  --help             Print usage
`), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		input    string
		stop     StopReason
		boundary int
	}{
		{"fixturebox run --rm --network host node curl --silent --show-error --fail --max-time 8 http://127.0.0.1:3000/api/v1/version", StopComplete, 5},
		{"fixturebox run node curl --silent", StopComplete, 2},
		{"fixturebox run node curl --network", StopComplete, 2},
		{"fixturebox run node curl --help is failing", StopComplete, 2},
		{"fixturebox run --network host --typo node curl --silent", StopUnknownOption, 4},
		{"fixturebox run --typo node curl --silent", StopUnknownOption, 2},
		{"fixturebox run --network", StopMissingOptionValue, 3},
		{"fixturebox run --network --rm node curl", StopMissingOptionValue, 3},
	} {
		t.Run(test.input, func(t *testing.T) {
			session := &fakeHelpSession{nodes: map[string]HelpResult{
				"": {Node: root, Status: HelpOK}, "run": {Node: run, Status: HelpOK},
			}}
			analysis := NewAnalyzer(&fakeHelpSource{session: session}).Analyze(context.Background(), invocation(test.input))
			if analysis.StopReason != test.stop || analysis.Boundary != test.boundary {
				t.Fatalf("analysis=%+v annotations=%+v", analysis, analysis.Annotations)
			}
			if test.stop == StopComplete {
				if analysis.Coverage != CoveragePartial || analysis.Uncertain() {
					t.Fatalf("forwarded command must remain partial without a structural veto: %+v", analysis)
				}
				for index := test.boundary; index < len(analysis.Annotations); index++ {
					if analysis.RoleAt(index) != RolePositional {
						t.Errorf("forwarded word %d was not kept inspectable: %+v", index, analysis.Annotations)
					}
				}
			}
			if strings.Join(session.calls, ",") != ",run" {
				t.Fatalf("forwarded command reached a help probe: %v", session.calls)
			}
		})
	}
}

func TestTraversalNeverProbesAnUndocumentedWord(t *testing.T) {
	t.Parallel()
	source := fixtureHelpSource()
	analysis := NewAnalyzer(source).Analyze(context.Background(), invocation("fixturevcs is status"))
	if analysis.Boundary != 1 || analysis.RoleAt(1) != RoleUnexpected || analysis.RoleAt(2) != RoleUnexpected {
		t.Fatalf("analysis skipped through undocumented word: %+v", analysis)
	}
	if got := strings.Join(source.session.calls, ","); got != "" {
		t.Fatalf("undocumented input reached help process: probes=%q", got)
	}
}

func TestUnavailableRootHelpIsUnmodeled(t *testing.T) {
	t.Parallel()
	source := &fakeHelpSource{session: &fakeHelpSession{nodes: map[string]HelpResult{"": {Status: HelpUnavailable}}}}
	analysis := NewAnalyzer(source).Analyze(context.Background(), invocation("anything subcommand"))
	if analysis.Modeled() || analysis.Uncertain() {
		t.Fatalf("unavailable help analysis = %+v", analysis)
	}
}

func TestUnavailableNestedHelpPreservesInspectableTail(t *testing.T) {
	t.Parallel()
	source := fixtureHelpSource()
	source.session.nodes["status"] = HelpResult{Status: HelpUnavailable}
	analysis := NewAnalyzer(source).Analyze(context.Background(), invocation("fixturevcs status is failing"))
	if analysis.Coverage != CoveragePartial || analysis.StopReason != StopHelpUnavailable || analysis.Uncertain() {
		t.Fatalf("nested unavailable analysis = %+v", analysis)
	}
	if analysis.RoleAt(2) != RolePositional || analysis.RoleAt(3) != RolePositional {
		t.Fatalf("nested unavailable tail was hidden: %+v", analysis.Annotations)
	}
}

func TestTruncatedHelpDoesNotInventAnUndocumentedSubcommand(t *testing.T) {
	t.Parallel()
	source := fixtureHelpSource()
	root := source.session.nodes[""]
	root.Node.Complete = false
	source.session.nodes[""] = root
	analysis := NewAnalyzer(source).Analyze(context.Background(), invocation("fixturevcs later feature"))
	if analysis.Coverage != CoveragePartial || analysis.StopReason != StopComplete || analysis.Uncertain() {
		t.Fatalf("truncated analysis=%+v", analysis)
	}
	if analysis.RoleAt(1) != RolePositional || analysis.RoleAt(2) != RolePositional {
		t.Fatalf("truncated help hid inspectable words: %+v", analysis.Annotations)
	}
}

func TestExplicitlyPartialCommandListDoesNotRejectUnlistedCommands(t *testing.T) {
	t.Parallel()
	source := fixtureHelpSource()
	root := source.session.nodes[""]
	root.Node.SubcommandsComplete = false
	source.session.nodes[""] = root
	analysis := NewAnalyzer(source).Analyze(context.Background(), invocation("fixturevcs extension --future"))
	if analysis.Coverage != CoveragePartial || analysis.StopReason != StopComplete || analysis.Uncertain() {
		t.Fatalf("partial-list analysis=%+v", analysis)
	}
	if analysis.RoleAt(1) != RolePositional || analysis.RoleAt(2) != RolePositional {
		t.Fatalf("unlisted extension tail was not left inspectable: %+v", analysis.Annotations)
	}
	if got := strings.Join(source.session.calls, ","); got != "" {
		t.Fatalf("unlisted extension was probed: %q", got)
	}
}

func TestDepthLimitFailsClosedWithoutProbingDeeper(t *testing.T) {
	t.Parallel()
	source := fixtureHelpSource()
	analyzer := NewAnalyzer(source)
	analyzer.maxDepth = 1
	analysis := analyzer.Analyze(context.Background(), invocation("fixturevcs remote add origin"))
	if analysis.StopReason != StopDepthLimit || !analysis.Uncertain() || strings.Join(source.session.calls, ",") != "" {
		t.Fatalf("depth-limited analysis=%+v probes=%v", analysis, source.session.calls)
	}
}

func fixtureHelpSource() *fakeHelpSource {
	root := NodeSpec{
		OptionsKnown: true,
		Options: map[string]OptionSpec{
			"--no-pager": {},
			"-C":         {Value: RequiredValue, AllowSeparate: true, AllowAttached: true},
			"--config":   {Value: RequiredValue, AllowAttached: true},
			"--help":     {Terminal: true},
		},
		SubcommandState: SubcommandsListed,
		Subcommands: map[string]struct{}{
			"status": {}, "commit": {}, "remote": {},
		},
		Complete:            true,
		SubcommandsComplete: true,
	}
	status := NodeSpec{
		OptionsKnown: true,
		Options: map[string]OptionSpec{
			"--short": {}, "--porcelain": {Value: OptionalValue, AllowAttached: true}, "--help": {Terminal: true},
		},
		SubcommandState: SubcommandsNone,
		Complete:        true,
	}
	commit := NodeSpec{
		OptionsKnown: true,
		Options: map[string]OptionSpec{
			"-a": {}, "-m": {Value: RequiredValue, AllowSeparate: true, AllowAttached: true}, "--message": {Value: RequiredValue, AllowSeparate: true, AllowAttached: true}, "--help": {Terminal: true},
		},
		SubcommandState: SubcommandsNone,
		Complete:        true,
	}
	remote := NodeSpec{
		OptionsKnown:        true,
		Options:             map[string]OptionSpec{},
		SubcommandState:     SubcommandsListed,
		Subcommands:         map[string]struct{}{"add": {}, "show": {}},
		Complete:            true,
		SubcommandsComplete: true,
	}
	leaf := NodeSpec{OptionsKnown: true, Options: map[string]OptionSpec{}, SubcommandState: SubcommandsNone, Complete: true}
	session := &fakeHelpSession{nodes: map[string]HelpResult{
		"":           {Node: root, Status: HelpOK},
		"status":     {Node: status, Status: HelpOK},
		"commit":     {Node: commit, Status: HelpOK},
		"remote":     {Node: remote, Status: HelpOK},
		"remote add": {Node: leaf, Status: HelpOK},
	}}
	return &fakeHelpSource{session: session}
}

type fakeHelpSource struct {
	session *fakeHelpSession
}

func (source *fakeHelpSource) Open(context.Context, ExecutableRef) (HelpSession, error) {
	return source.session, nil
}

type fakeHelpSession struct {
	nodes map[string]HelpResult
	calls []string
}

func (session *fakeHelpSession) Load(_ context.Context, prefix []string) HelpResult {
	key := strings.Join(prefix, " ")
	session.calls = append(session.calls, key)
	if result, ok := session.nodes[key]; ok {
		return result
	}
	return HelpResult{Status: HelpUnavailable}
}

func (*fakeHelpSession) Close() error { return nil }

func invocation(input string) Invocation {
	parts := strings.Fields(input)
	words := make([]Word, len(parts))
	for index, part := range parts {
		quoted := strings.HasPrefix(part, `"`) || strings.HasPrefix(part, `'`)
		words[index] = Word{Text: strings.Trim(part, `'"`), Static: !quoted, Quoted: quoted}
	}
	return Invocation{Words: words}
}
