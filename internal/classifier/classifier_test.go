package classifier

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agenticlab-ai/humansh/internal/commandgrammar"
	"github.com/agenticlab-ai/humansh/internal/config"
	"github.com/agenticlab-ai/humansh/internal/shell"
)

func classifierWithFixtureGrammar() Classifier {
	return Classifier{Invocations: commandgrammar.NewAnalyzer(classifierHelpSource{})}
}

type classifierHelpSource struct{}

func (classifierHelpSource) Open(context.Context, commandgrammar.ExecutableRef) (commandgrammar.HelpSession, error) {
	return classifierHelpSession{}, nil
}

type classifierHelpSession struct{}

func (classifierHelpSession) Load(_ context.Context, prefix []string) commandgrammar.HelpResult {
	root := commandgrammar.NodeSpec{
		OptionsKnown: true,
		Options: map[string]commandgrammar.OptionSpec{
			"--help":     {Terminal: true},
			"--no-pager": {},
			"-C":         {Value: commandgrammar.RequiredValue, AllowSeparate: true, AllowAttached: true},
		},
		SubcommandState:     commandgrammar.SubcommandsListed,
		Subcommands:         map[string]struct{}{"status": {}, "commit": {}, "attach": {}, "test": {}, "help": {}, "run": {}},
		UnprobedSubcommands: map[string]struct{}{"help": {}},
		Complete:            true,
		SubcommandsComplete: true,
	}
	status := commandgrammar.NodeSpec{
		OptionsKnown: true,
		Options: map[string]commandgrammar.OptionSpec{
			"--help": {Terminal: true}, "--short": {},
		},
		SubcommandState: commandgrammar.SubcommandsNone,
		Complete:        true,
	}
	commit := commandgrammar.NodeSpec{
		OptionsKnown: true,
		Options: map[string]commandgrammar.OptionSpec{
			"-m": {Value: commandgrammar.RequiredValue, AllowSeparate: true, AllowAttached: true},
		},
		SubcommandState: commandgrammar.SubcommandsNone,
		Complete:        true,
	}
	attach := commandgrammar.NodeSpec{
		OptionsKnown: true,
		Options: map[string]commandgrammar.OptionSpec{
			"-c": {}, "--create": {},
		},
		SubcommandState:     commandgrammar.SubcommandsListed,
		Subcommands:         map[string]struct{}{"options": {}, "help": {}},
		SubcommandsComplete: true,
		AcceptsPositionals:  true,
		Complete:            true,
	}
	goTest, err := commandgrammar.ParseHelp([]byte("usage: go test [build/test flags] [packages] [build/test flags & test binary flags]\n"), true)
	if err != nil {
		return commandgrammar.HelpResult{Status: commandgrammar.HelpUnparseable}
	}
	var node commandgrammar.NodeSpec
	switch strings.Join(prefix, " ") {
	case "":
		node = root
	case "status":
		node = status
	case "commit":
		node = commit
	case "attach":
		node = attach
	case "test":
		node = goTest
	case "run":
		node, err = commandgrammar.ParseHelp([]byte("Usage: fixturevcs run [OPTIONS] TARGET [COMMAND] [ARG...]\n\nOptions:\n  --network network  Connect to a network\n  --rm               Remove after exit\n"), true)
		if err != nil {
			return commandgrammar.HelpResult{Status: commandgrammar.HelpUnparseable}
		}
	default:
		return commandgrammar.HelpResult{Status: commandgrammar.HelpUnavailable}
	}
	return commandgrammar.HelpResult{Node: node, Status: commandgrammar.HelpOK}
}

func (classifierHelpSession) Close() error { return nil }

func TestPureClassifierP95Target(t *testing.T) {
	classifier := Classifier{}
	durations := make([]time.Duration, 1000)
	for index := range durations {
		started := time.Now()
		result := classifier.Classify(Input{Raw: "find all files modified today", Shell: "zsh", FirstTokenKind: shell.TokenCommand, Overrides: config.DefaultOverrides()})
		durations[index] = time.Since(started)
		if result.Outcome != Ambiguous {
			t.Fatalf("classification=%+v", result)
		}
	}
	slices.Sort(durations)
	p95 := durations[(len(durations)*95/100)-1]
	if p95 >= 10*time.Millisecond {
		t.Fatalf("pure classifier p95 %s exceeds 10ms target", p95)
	}
}

func TestNormativeExamples(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		raw     string
		kind    shell.FirstTokenKind
		grammar bool
		want    Classification
	}{
		{"structured", "fixturevcs status", shell.TokenCommand, true, Literal},
		{"English-subcommand", "fixturevcs is failing please authenticate", shell.TokenCommand, true, Ambiguous},
		{"English-operands", "fixturevcs status is failing please authenticate", shell.TokenCommand, true, Ambiguous},
		{"quoted-option-value", `fixturevcs commit -m "please authenticate"`, shell.TokenCommand, true, Literal},
		{"positionals-alongside-subcommands", "zellij attach -c pyxis-codex -- codex", shell.TokenCommand, true, Literal},
		{"English-positional-alongside-subcommands", "zellij attach is failing please authenticate", shell.TokenCommand, true, Ambiguous},
		{"subcommand-typo", "fixturevcs statsu", shell.TokenCommand, true, Ambiguous},
		{"flags-path", "ls -lah ~/Downloads", shell.TokenCommand, false, Literal},
		{"assignment", "FOO=bar", shell.TokenUnresolved, false, Literal},
		{"pipeline", "cat file.txt | grep error", shell.TokenCommand, false, Literal},
		{"bare-builtin-English-tail", "echo show me the files", shell.TokenBuiltin, false, Ambiguous},
		{"echo-quoted", `echo "show me the files"`, shell.TokenBuiltin, false, Literal},
		{"which", "which git", shell.TokenCommand, false, Literal},
		{"open-file", "open README.md", shell.TokenCommand, false, Literal},
		{"find-command", "find . -type f -mtime -1", shell.TokenCommand, false, Literal},
		{"instruction", "show me the largest files in this folder", shell.TokenUnresolved, false, Natural},
		{"question", "how do I see what is listening on port 3000", shell.TokenUnresolved, false, Natural},
		{"list", "list all files changed during the last two days", shell.TokenUnresolved, false, Natural},
		{"find-tail", "find all files modified today", shell.TokenCommand, false, Ambiguous},
		{"which-tail", "which process is using port 3000", shell.TokenCommand, false, Ambiguous},
		{"open-tail", "open the project folder", shell.TokenCommand, false, Ambiguous},
		{"sort-tail", "sort these files by size", shell.TokenCommand, false, Ambiguous},
		{"kill-tail", "kill whatever is using port 3000", shell.TokenBuiltin, false, Ambiguous},
		{"time-tail", "time the build", shell.TokenReserved, false, Ambiguous},
		{"watch-tail", "watch the logs", shell.TokenCommand, false, Ambiguous},
		{"top-tail", "top processes by memory", shell.TokenCommand, false, Ambiguous},
		{"who-tail", "who is using port 80", shell.TokenCommand, false, Ambiguous},
		{"make-tail", "make it faster", shell.TokenCommand, false, Ambiguous},
		{"head-tail", "head to the downloads folder", shell.TokenCommand, false, Ambiguous},
		{"test-tail", "test if the port is open", shell.TokenBuiltin, false, Ambiguous},
		{"typo", "gti status", shell.TokenUnresolved, false, Natural},
		{"custom", "foo bar baz", shell.TokenUnresolved, false, Natural},
		{"redirect", "not-a-command > existing-file", shell.TokenUnresolved, false, Literal},
		{"no-command-exception", "docker ps that were running", shell.TokenCommand, false, Ambiguous},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			classifier := Classifier{}
			if test.grammar {
				classifier = classifierWithFixtureGrammar()
			}
			got := classifier.Classify(Input{Raw: test.raw, Shell: "zsh", FirstTokenKind: test.kind, Overrides: config.DefaultOverrides()})
			if got.Outcome != test.want {
				t.Fatalf("Classify(%q) = %s (command=%d english=%d evidence=%+v), want %s", test.raw, got.Outcome, got.CommandScore, got.EnglishScore, got.Evidence, test.want)
			}
		})
	}
}

func TestGrammarLexiconLoadBearingRows(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"find all files modified today", "make it faster"} {
		result := (Classifier{}).Classify(Input{Raw: raw, FirstTokenKind: shell.TokenCommand})
		if !hasEvidence(result, "natural_language_tail") {
			t.Fatalf("%q did not receive natural_language_tail: %+v", raw, result.Evidence)
		}
	}
}

func TestCommandGrammarIsSharedAcrossShellInputs(t *testing.T) {
	t.Parallel()
	classifier := classifierWithFixtureGrammar()
	for _, shellID := range []string{"zsh", "bash"} {
		t.Run(shellID, func(t *testing.T) {
			t.Parallel()
			result := classifier.Classify(Input{Raw: "fixturevcs is failing please authenticate", Shell: shellID, FirstTokenKind: shell.TokenCommand})
			if result.Version != resultVersion || result.Outcome != Ambiguous || result.CommandGrammar == nil {
				t.Fatalf("classification = %+v", result)
			}
			if result.CommandGrammar.Source != "installed_help" || result.CommandGrammar.Boundary != 1 || result.CommandGrammar.StopReason != commandgrammar.StopUndocumentedSubcommand {
				t.Fatalf("grammar summary = %+v", result.CommandGrammar)
			}
			for _, code := range []string{"resolved_first_token", "command_grammar_undocumented_subcommand", "natural_language_tail"} {
				if !hasEvidence(result, code) {
					t.Errorf("missing evidence %q: %+v", code, result.Evidence)
				}
			}
		})
	}
}

func TestCommandGrammarExcludesKnownOptionValuesFromEnglishTail(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{`fixturevcs commit -m "please authenticate"`} {
		result := classifierWithFixtureGrammar().Classify(Input{Raw: raw, FirstTokenKind: shell.TokenCommand})
		if result.Outcome != Literal || result.CommandGrammar == nil || hasEvidence(result, "natural_language_tail") {
			t.Fatalf("Classify(%q) = %+v", raw, result)
		}
	}
}

func TestForwardedCommandClassificationAcrossShells(t *testing.T) {
	t.Parallel()
	for _, shellID := range []string{"zsh", "bash"} {
		for _, test := range []struct {
			input string
			want  Classification
		}{
			{"fixturevcs run --rm --network host node curl --silent --max-time 8 http://127.0.0.1:3000/api/v1/version", Literal},
			{"fixturevcs run --network host --typo node curl --silent", Ambiguous},
			{"fixturevcs run --network", Ambiguous},
			{"fixturevcs run node is failing please authenticate", Ambiguous},
		} {
			t.Run(shellID+"/"+test.input, func(t *testing.T) {
				result := classifierWithFixtureGrammar().Classify(Input{Raw: test.input, Shell: shellID, FirstTokenKind: shell.TokenCommand})
				if result.Outcome != test.want || result.CommandGrammar == nil {
					t.Fatalf("classification=%+v, want %s", result, test.want)
				}
			})
		}
	}
}

func TestCommandGrammarInspectsWordsAfterTerminalOptions(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"fixturevcs --help is failing please authenticate",
		"fixturevcs status --help is failing please authenticate",
	} {
		result := classifierWithFixtureGrammar().Classify(Input{Raw: raw, FirstTokenKind: shell.TokenCommand})
		if result.Outcome != Ambiguous || result.CommandGrammar == nil || !hasEvidence(result, "natural_language_tail") {
			t.Fatalf("Classify(%q) = %+v", raw, result)
		}
	}
}

func TestCommandGrammarJSONSummaryOmitsRawWordsAndAnnotations(t *testing.T) {
	t.Parallel()
	result := classifierWithFixtureGrammar().Classify(Input{Raw: "fixturevcs is failing please authenticate", FirstTokenKind: shell.TokenCommand})
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "failing please authenticate") || strings.Contains(text, `"annotations"`) {
		t.Fatalf("classification JSON exposed raw grammar words or internal roles: %s", text)
	}
}

func TestClassifierHasNoCommandNameNegativeExceptions(t *testing.T) {
	t.Parallel()
	result := (Classifier{}).Classify(Input{Raw: "docker ps that were running", FirstTokenKind: shell.TokenCommand})
	for _, code := range []string{"natural_language_tail", "natural_clause", "mostly_ordinary_words"} {
		if !hasEvidence(result, code) {
			t.Fatalf("command-name exception suppressed %s: %+v", code, result.Evidence)
		}
	}
}

func TestInstalledHelpAnalyzerRunsOnlyForExternalCommands(t *testing.T) {
	t.Parallel()
	for _, kind := range []shell.FirstTokenKind{shell.TokenAlias, shell.TokenFunction, shell.TokenBuiltin, shell.TokenReserved, shell.TokenUnresolved} {
		counter := &countingInvocationAnalyzer{}
		(Classifier{Invocations: counter}).Classify(Input{Raw: "fixturevcs status", FirstTokenKind: kind, ResolvedCommandPath: "/tmp/fixturevcs"})
		if counter.calls != 0 {
			t.Errorf("kind=%s invoked installed help", kind)
		}
	}
	counter := &countingInvocationAnalyzer{}
	(Classifier{Invocations: counter}).Classify(Input{Raw: "fixturevcs status", FirstTokenKind: shell.TokenCommand, ResolvedCommandPath: "/tmp/fixturevcs"})
	if counter.calls != 1 || counter.path != "/tmp/fixturevcs" {
		t.Fatalf("external command metadata calls=%d path=%q", counter.calls, counter.path)
	}
}

type countingInvocationAnalyzer struct {
	calls int
	path  string
}

func (analyzer *countingInvocationAnalyzer) Analyze(_ context.Context, inv commandgrammar.Invocation) commandgrammar.Analysis {
	analyzer.calls++
	analyzer.path = inv.ExecutablePath
	return commandgrammar.Analysis{Coverage: commandgrammar.CoverageUnmodeled}
}

func TestOverrides(t *testing.T) {
	t.Parallel()
	overrides := config.ClassifierOverrides{Version: 1, AlwaysCommands: []string{"deploy"}, AlwaysNaturalLanguagePrefixes: []string{"explain how to"}}
	classifier := Classifier{}
	if got := classifier.Classify(Input{Raw: "deploy production", FirstTokenKind: shell.TokenUnresolved, Overrides: overrides}); got.Outcome != Literal {
		t.Fatalf("command override = %s", got.Outcome)
	}
	if got := classifier.Classify(Input{Raw: "Explain   how to list files", FirstTokenKind: shell.TokenUnresolved, Overrides: overrides}); got.Outcome != Natural {
		t.Fatalf("English override = %s", got.Outcome)
	}
}

func TestDecisionCodeUsesStableVocabulary(t *testing.T) {
	allowed := map[string]bool{
		"strong_command_weak_english": true,
		"strong_english_weak_command": true,
		"conflicting_strong_evidence": true,
		"insufficient_evidence":       true,
		"command_grammar_uncertain":   true,
	}
	overrides := config.ClassifierOverrides{Version: 1, AlwaysCommands: []string{"deploy"}, AlwaysNaturalLanguagePrefixes: []string{"explain"}}
	for _, input := range []Input{
		{Raw: "", FirstTokenKind: shell.TokenEmpty},
		{Raw: "# comment", FirstTokenKind: shell.TokenUnknown},
		{Raw: "one\ntwo", FirstTokenKind: shell.TokenUnknown},
		{Raw: "deploy now", FirstTokenKind: shell.TokenUnknown, Overrides: overrides},
		{Raw: "explain files", FirstTokenKind: shell.TokenUnknown, Overrides: overrides},
		{Raw: "git status", FirstTokenKind: shell.TokenCommand},
	} {
		result := (Classifier{}).Classify(input)
		if !allowed[result.DecisionCode] {
			t.Errorf("raw=%q decision=%q evidence=%+v", input.Raw, result.DecisionCode, result.Evidence)
		}
		decisionIndex := -1
		for index, evidence := range result.Evidence {
			if evidence.Domain == DecisionEvidence {
				decisionIndex = index
				break
			}
		}
		if decisionIndex < 0 || result.Evidence[decisionIndex].Code != result.DecisionCode || result.Evidence[decisionIndex].Weight != 0 {
			t.Errorf("primary decision evidence is not first for raw=%q: %+v", input.Raw, result.Evidence)
		}
	}
}

func TestFocusedScannerAndEvidenceInvariants(t *testing.T) {
	classifier := Classifier{}
	tests := []struct {
		name      string
		first     Input
		second    *Input
		required  []string
		forbidden []string
	}{
		{name: "quoted-operators-have-no-unquoted-evidence", first: Input{Raw: `echo 'show me files | *.go --all'`, FirstTokenKind: shell.TokenBuiltin}, required: []string{"quoted_argument"}, forbidden: []string{"shell_operator", "glob_syntax", "conventional_flag", "natural_instruction_prefix"}},
		{name: "escaped-glob-is-not-expanded", first: Input{Raw: `print \*.go`, FirstTokenKind: shell.TokenBuiltin}, forbidden: []string{"glob_syntax"}},
		{name: "sentence-question-is-not-a-glob", first: Input{Raw: "show me files?", FirstTokenKind: shell.TokenUnresolved}, required: []string{"question_mark"}, forbidden: []string{"glob_syntax"}},
		{name: "filename-question-is-a-glob", first: Input{Raw: "print file?.txt", FirstTokenKind: shell.TokenBuiltin}, required: []string{"glob_syntax"}, forbidden: []string{"question_mark"}},
		{name: "malformed-quote-does-not-create-English-intent", first: Input{Raw: `echo "show me files`, FirstTokenKind: shell.TokenBuiltin}, forbidden: []string{"natural_instruction_prefix", "natural_language_tail", "mostly_ordinary_words"}},
		{name: "assignment-disqualifies-mostly-ordinary", first: Input{Raw: "FOO=bar show me files", FirstTokenKind: shell.TokenUnresolved}, required: []string{"assignment_prefix"}, forbidden: []string{"mostly_ordinary_words"}},
		{name: "non-head-assignment-disqualifies-English-tail", first: Input{Raw: "find target=value in folder", FirstTokenKind: shell.TokenCommand}, forbidden: []string{"natural_language_tail", "natural_clause", "mostly_ordinary_words"}},
		{name: "grammar-tail-counts-from-non-alphabetic-head", first: Input{Raw: "probe-tool all files", FirstTokenKind: shell.TokenCommand}, required: []string{"natural_language_tail"}},
		{name: "clause-matches-whole-words", first: Input{Raw: "probe into theater folder", FirstTokenKind: shell.TokenCommand}, forbidden: []string{"natural_clause"}},
		{name: "sentence-question-allows-trailing-space", first: Input{Raw: "show me files?  ", FirstTokenKind: shell.TokenUnresolved}, required: []string{"question_mark"}, forbidden: []string{"glob_syntax"}},
		{name: "semantic-whitespace", first: Input{Raw: "show me files changed today", FirstTokenKind: shell.TokenUnresolved}, second: &Input{Raw: "  show   me  files changed today  ", FirstTokenKind: shell.TokenUnresolved}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := test.first.Raw
			first := classifier.Classify(test.first)
			if test.first.Raw != before {
				t.Fatalf("classifier mutated raw input: before=%q after=%q", before, test.first.Raw)
			}
			for _, code := range test.required {
				if !hasEvidence(first, code) {
					t.Errorf("required evidence %q missing: %+v", code, first.Evidence)
				}
			}
			for _, code := range test.forbidden {
				if hasEvidence(first, code) {
					t.Errorf("forbidden evidence %q present: %+v", code, first.Evidence)
				}
			}
			if test.second != nil {
				second := classifier.Classify(*test.second)
				if first.Outcome != second.Outcome || first.CommandScore != second.CommandScore || first.EnglishScore != second.EnglishScore || first.DecisionCode != second.DecisionCode || !slices.Equal(first.Evidence, second.Evidence) {
					t.Fatalf("whitespace changed semantic result:\nfirst=%+v\nsecond=%+v", first, second)
				}
			}
		})
	}
}

func FuzzClassifier(f *testing.F) {
	f.Add("git status")
	f.Add("show me files")
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 8192 {
			t.Skip()
		}
		result := (Classifier{}).Classify(Input{Raw: raw, FirstTokenKind: shell.TokenUnknown})
		switch result.Outcome {
		case Literal, Natural, Ambiguous:
		default:
			t.Fatalf("invalid outcome %q", result.Outcome)
		}
	})
}

func BenchmarkClassifier(b *testing.B) {
	classifier := Classifier{}
	in := Input{Raw: "find all files modified today", FirstTokenKind: shell.TokenCommand}
	for range b.N {
		_ = classifier.Classify(in)
	}
}

func hasEvidence(result Result, code string) bool {
	for _, evidence := range result.Evidence {
		if evidence.Code == code {
			return true
		}
	}
	return false
}
