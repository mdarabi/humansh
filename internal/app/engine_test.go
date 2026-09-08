package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/agenticlab-ai/humansh/internal/classifier"
	"github.com/agenticlab-ai/humansh/internal/config"
	usererr "github.com/agenticlab-ai/humansh/internal/errors"
	"github.com/agenticlab-ai/humansh/internal/llm"
	"github.com/agenticlab-ai/humansh/internal/risk"
	"github.com/agenticlab-ai/humansh/internal/shell"
	"github.com/agenticlab-ai/humansh/internal/shell/protocol"
)

type fakeProvider struct {
	response llm.TranslationResponse
	calls    int
	request  llm.TranslationRequest
}

func (p *fakeProvider) ID() llm.ProviderID { return llm.Codex }
func (p *fakeProvider) Diagnose(context.Context) llm.Diagnostic {
	return llm.Diagnostic{Available: true}
}
func (p *fakeProvider) Probe(context.Context) llm.Diagnostic {
	return llm.Diagnostic{Installed: true, Configured: true, Authenticated: true, Available: true, LiveCheck: true, AuthMode: "test"}
}
func (p *fakeProvider) Translate(_ context.Context, request llm.TranslationRequest) (llm.TranslationResponse, error) {
	p.calls++
	p.request = request
	return p.response, nil
}

type fakeShell struct{}

func (fakeShell) ID() shell.ID                                    { return shell.Zsh }
func (fakeShell) Diagnose(context.Context) shell.Diagnostic       { return shell.Diagnostic{Available: true} }
func (fakeShell) Capabilities() shell.Capabilities                { return shell.Capabilities{} }
func (fakeShell) PromptProfile() shell.PromptProfile              { return shell.PromptProfile{Shell: "zsh"} }
func (fakeShell) ValidateGenerated(context.Context, string) error { return nil }
func (fakeShell) NormalizeGenerated(value string) (string, error) { return value, nil }
func (fakeShell) IntegrationAsset() ([]byte, bool)                { return nil, false }
func (fakeShell) SupportedProtocols() []string                    { return []string{protocol.Version} }

func testEngine(provider *fakeProvider) Engine {
	return Engine{Classifier: classifier.Classifier{}, Providers: llm.MapRegistry{llm.Codex: provider}, Shells: shell.MapRegistry{shell.Zsh: fakeShell{}}, Context: fixedRuntimeContext{label: "test"}}
}

type fixedRuntimeContext struct {
	label string
}

func (c fixedRuntimeContext) WorkingDirectoryLabel(string, string) string { return c.label }

type recordingValidator struct {
	responses int
	commands  int
}

func (v *recordingValidator) Response(llm.TranslationResponse) error { v.responses++; return nil }
func (v *recordingValidator) Command(string) error                   { v.commands++; return nil }

type recordingRisk struct {
	commands []string
}

type contextClassifier struct{}

func (contextClassifier) ClassifyContext(ctx context.Context, _ classifier.Input) classifier.Result {
	<-ctx.Done()
	return classifier.Result{Outcome: classifier.Literal}
}

func (r *recordingRisk) Analyze(command string) risk.Result {
	r.commands = append(r.commands, command)
	return risk.Result{Level: risk.Medium, Reasons: []string{"fake_state_change"}}
}

func TestSmartDoesNotCallProviderForLiteralOrAmbiguous(t *testing.T) {
	t.Parallel()
	provider := &fakeProvider{}
	engine := testEngine(provider)
	cfg := config.Default()
	literal, err := engine.Smart(context.Background(), RuntimeRequest{Input: "git status", ShellID: shell.Zsh, FirstTokenKind: shell.TokenCommand, Config: cfg})
	if err != nil || literal.ExitCode != protocol.ExitLiteral {
		t.Fatalf("literal=%+v err=%v", literal, err)
	}
	ambiguous, err := engine.Smart(context.Background(), RuntimeRequest{Input: "find all files modified today", ShellID: shell.Zsh, FirstTokenKind: shell.TokenCommand, Config: cfg})
	if err != nil || ambiguous.ExitCode != protocol.ExitAmbiguous {
		t.Fatalf("ambiguous=%+v err=%v", ambiguous, err)
	}
	if ambiguous.Message != "Not sure whether this is English or a command. Next: press Ctrl-G to translate it, or press Ctrl-X then Enter to run it unchanged." {
		t.Fatalf("ambiguous message is not actionable: %q", ambiguous.Message)
	}
	for _, shellID := range []shell.ID{shell.Zsh, shell.Bash} {
		ambiguousTail, err := engine.Smart(context.Background(), RuntimeRequest{Input: "fixturevcs is failing please authenticate", ShellID: shellID, FirstTokenKind: shell.TokenCommand, Config: cfg})
		if err != nil || ambiguousTail.ExitCode != protocol.ExitAmbiguous || ambiguousTail.Classification == nil {
			t.Fatalf("shell=%s ambiguous tail=%+v err=%v", shellID, ambiguousTail, err)
		}
	}
	if provider.calls != 0 {
		t.Fatalf("provider calls=%d", provider.calls)
	}
}

func TestSmartPropagatesClassificationCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (Engine{Classifier: contextClassifier{}}).Smart(ctx, RuntimeRequest{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Smart cancellation error=%v", err)
	}
}

func TestTranslateNonGenerationMessagesGiveANextStep(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		response llm.TranslationResponse
		wantExit int
		want     string
	}{
		{
			name:     "clarification",
			response: llm.TranslationResponse{Status: "clarify", Clarification: "Which folder should I search?", Assumptions: []string{}},
			wantExit: protocol.ExitClarify,
			want:     "Next: add that detail to your request, then try again.",
		},
		{
			name:     "unsupported",
			response: llm.TranslationResponse{Status: "unsupported", Explanation: "This needs more than one command.", Assumptions: []string{}},
			wantExit: protocol.ExitUnsupported,
			want:     "Next: edit the request and try again, or press Ctrl-X then Ctrl-T to run it unchanged.",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			provider := &fakeProvider{response: test.response}
			engine := testEngine(provider)
			cfg := config.Default()
			cfg.Shell.ForceLiteralBinding = "^X^T"
			result, err := engine.Translate(context.Background(), RuntimeRequest{Input: "test request", ShellID: shell.Zsh, Config: cfg})
			if err != nil {
				t.Fatal(err)
			}
			if result.ExitCode != test.wantExit || !strings.Contains(result.Message, test.want) {
				t.Fatalf("result=%+v, want exit %d and message containing %q", result, test.wantExit, test.want)
			}
		})
	}
}

func TestTranslateReturnsReviewedRiskResult(t *testing.T) {
	t.Parallel()
	provider := &fakeProvider{response: llm.TranslationResponse{Status: "ok", Command: "rm -rf build", Explanation: "Deletes build output.", Assumptions: []string{}}}
	engine := testEngine(provider)
	cfg := config.Default()
	result, err := engine.Translate(context.Background(), RuntimeRequest{Input: "delete build", ShellID: shell.Zsh, FirstTokenKind: shell.TokenUnresolved, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != protocol.ExitGeneratedHigh || result.Command != "rm -rf build" {
		t.Fatalf("result=%+v", result)
	}
	if provider.calls != 1 {
		t.Fatalf("calls=%d", provider.calls)
	}
}

func TestTranslateValidatesExactProviderBytesBeforeNormalization(t *testing.T) {
	provider := &fakeProvider{response: llm.TranslationResponse{Status: "ok", Command: "ls\n", Explanation: "Lists files.", Assumptions: []string{}}}
	engine := testEngine(provider)
	_, err := engine.Translate(context.Background(), RuntimeRequest{Input: "list files", ShellID: shell.Zsh, Config: config.Default()})
	typed, ok := usererr.As(err)
	if !ok || typed.ExitCode != protocol.ExitPolicyRejected {
		t.Fatalf("newline was normalized into an accepted command: %#v", err)
	}
}

func TestWorkingContextDoesNotExposeHomeBasename(t *testing.T) {
	provider := &fakeProvider{response: llm.TranslationResponse{Status: "ok", Command: "pwd", Explanation: "Prints directory.", Assumptions: []string{}}}
	engine := testEngine(provider)
	engine.Context = fixedRuntimeContext{label: "~"}
	cfg := config.Default()
	_, err := engine.Translate(context.Background(), RuntimeRequest{Input: "where am I", ShellID: shell.Zsh, Config: cfg, WorkingDir: "/not-inspected-by-app"})
	if err != nil {
		t.Fatal(err)
	}
	if provider.request.WorkingContext != "~" {
		t.Fatalf("working context=%q", provider.request.WorkingContext)
	}
	if provider.request.Input != "where am I" {
		t.Fatal("input changed")
	}
}

type replacementProvider struct {
	id      llm.ProviderID
	calls   int
	request llm.TranslationRequest
}

func (p *replacementProvider) ID() llm.ProviderID { return p.id }
func (*replacementProvider) Diagnose(context.Context) llm.Diagnostic {
	return llm.Diagnostic{Available: true}
}
func (*replacementProvider) Probe(context.Context) llm.Diagnostic {
	return llm.Diagnostic{Installed: true, Configured: true, Authenticated: true, Available: true, LiveCheck: true, AuthMode: "test"}
}
func (p *replacementProvider) Translate(_ context.Context, request llm.TranslationRequest) (llm.TranslationResponse, error) {
	p.calls++
	p.request = request
	return llm.TranslationResponse{Status: "ok", Command: "replacement-command", Explanation: "Uses replacement adapters.", Assumptions: []string{}}, nil
}

type replacementShell struct {
	id        shell.ID
	validated string
}

func (s *replacementShell) ID() shell.ID { return s.id }
func (*replacementShell) Diagnose(context.Context) shell.Diagnostic {
	return shell.Diagnostic{Available: true}
}
func (*replacementShell) Capabilities() shell.Capabilities { return shell.Capabilities{} }
func (s *replacementShell) PromptProfile() shell.PromptProfile {
	return shell.PromptProfile{Shell: string(s.id)}
}
func (s *replacementShell) ValidateGenerated(_ context.Context, command string) error {
	s.validated = command
	return nil
}
func (*replacementShell) NormalizeGenerated(value string) (string, error) { return value, nil }
func (*replacementShell) IntegrationAsset() ([]byte, bool)                { return nil, false }
func (*replacementShell) SupportedProtocols() []string                    { return []string{"replacement-v1"} }

func TestEngineSelectsReplacementAdaptersOnlyThroughRegistries(t *testing.T) {
	const fourth llm.ProviderID = "fourth"
	const second shell.ID = "second"
	provider := &replacementProvider{id: fourth}
	shellAdapter := &replacementShell{id: second}
	engine := Engine{Classifier: classifier.Classifier{}, Providers: llm.MapRegistry{fourth: provider}, Shells: shell.MapRegistry{second: shellAdapter}}
	cfg := config.Default()
	cfg.Provider = fourth
	result, err := engine.Translate(context.Background(), RuntimeRequest{Input: "replacement request", ShellID: second, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 || provider.request.Shell != string(second) || shellAdapter.validated != "replacement-command" || result.Command != "replacement-command" {
		t.Fatalf("provider calls=%d target shell=%q shell command=%q result=%+v", provider.calls, provider.request.Shell, shellAdapter.validated, result)
	}
}

func TestEngineUsesInjectedValidationAndRiskServices(t *testing.T) {
	provider := &fakeProvider{response: llm.TranslationResponse{Status: "ok", Command: "custom command", Assumptions: []string{}}}
	validator := &recordingValidator{}
	riskAnalyzer := &recordingRisk{}
	engine := testEngine(provider)
	engine.Validator = validator
	engine.Risk = riskAnalyzer
	result, err := engine.Translate(context.Background(), RuntimeRequest{Input: "request", ShellID: shell.Zsh, Config: config.Default()})
	if err != nil {
		t.Fatal(err)
	}
	if validator.responses != 1 || validator.commands != 1 || len(riskAnalyzer.commands) != 1 || result.ExitCode != protocol.ExitGeneratedMedium {
		t.Fatalf("validator=%+v risk=%+v result=%+v", validator, riskAnalyzer, result)
	}
	analyzed, err := engine.Analyze(context.Background(), RuntimeRequest{Input: "analyzed command", ShellID: shell.Zsh, Config: config.Default()})
	if err != nil {
		t.Fatal(err)
	}
	if validator.responses != 1 || validator.commands != 2 || len(riskAnalyzer.commands) != 2 || riskAnalyzer.commands[1] != "analyzed command" || analyzed.ExitCode != protocol.ExitGeneratedMedium {
		t.Fatalf("analyze did not use injected services: validator=%+v risk=%+v result=%+v", validator, riskAnalyzer, analyzed)
	}
}
