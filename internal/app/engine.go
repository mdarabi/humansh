package app

import (
	"context"
	"fmt"
	"runtime"

	"github.com/agenticlab-ai/humansh/internal/classifier"
	"github.com/agenticlab-ai/humansh/internal/config"
	usererr "github.com/agenticlab-ai/humansh/internal/errors"
	"github.com/agenticlab-ai/humansh/internal/llm"
	"github.com/agenticlab-ai/humansh/internal/risk"
	"github.com/agenticlab-ai/humansh/internal/shell"
	"github.com/agenticlab-ai/humansh/internal/shell/protocol"
	"github.com/agenticlab-ai/humansh/internal/validate"
)

type Classifier interface {
	ClassifyContext(context.Context, classifier.Input) classifier.Result
}
type LocalValidator interface {
	Response(llm.TranslationResponse) error
	Command(string) error
}
type RiskAnalyzer interface{ Analyze(string) risk.Result }
type RuntimeContext interface {
	WorkingDirectoryLabel(mode, cwd string) string
}

type Engine struct {
	Classifier Classifier
	Providers  llm.Registry
	Shells     shell.Registry
	Validator  LocalValidator
	Risk       RiskAnalyzer
	Context    RuntimeContext
}

type RuntimeRequest struct {
	Input               string
	ShellID             shell.ID
	FirstTokenKind      shell.FirstTokenKind
	ResolvedCommandPath string
	WorkingDir          string
	Config              config.RuntimeConfig
	Overrides           config.ClassifierOverrides
}

type Result struct {
	ExitCode       int                `json:"exit_code"`
	Command        string             `json:"command,omitempty"`
	Message        string             `json:"message,omitempty"`
	Explanation    string             `json:"explanation,omitempty"`
	Classification *classifier.Result `json:"classification,omitempty"`
	Risk           *risk.Result       `json:"risk,omitempty"`
}

func (e Engine) Smart(ctx context.Context, request RuntimeRequest) (Result, error) {
	classification := e.Classifier.ClassifyContext(ctx, classifier.Input{Raw: request.Input, Shell: string(request.ShellID), FirstTokenKind: request.FirstTokenKind, ResolvedCommandPath: request.ResolvedCommandPath, Overrides: request.Overrides})
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	switch classification.Outcome {
	case classifier.Literal:
		return Result{ExitCode: protocol.ExitLiteral, Classification: &classification}, nil
	case classifier.Ambiguous:
		return Result{
			ExitCode: protocol.ExitAmbiguous,
			Message: fmt.Sprintf(
				"Not sure whether this is English or a command. Next: press %s to translate it, or press %s to run it unchanged.",
				config.BindingLabel(request.Config.Shell.ForceTranslateBinding),
				config.BindingLabel(request.Config.Shell.ForceLiteralBinding),
			),
			Classification: &classification,
		}, nil
	case classifier.Natural:
		result, err := e.Translate(ctx, request)
		result.Classification = &classification
		return result, err
	default:
		return Result{}, usererr.WithExit(protocol.ExitInternal, "classifier_invalid", "Classifier returned an invalid outcome.", "Nothing was changed or executed.", false, nil)
	}
}

func (e Engine) Translate(ctx context.Context, request RuntimeRequest) (Result, error) {
	provider, ok := e.Providers.Get(request.Config.Provider)
	if !ok {
		return Result{}, usererr.WithExit(protocol.ExitProviderUnavailable, "provider_unknown", "Configured provider is unavailable.", "Nothing was changed or executed.", false, nil, usererr.Fix{Description: "List configured providers with", Command: "humansh provider list"})
	}
	adapter, ok := e.Shells.Get(request.ShellID)
	if !ok {
		return Result{}, usererr.WithExit(protocol.ExitConfig, "shell_unknown", "Configured shell adapter is unavailable.", "Nothing was changed or executed.", false, nil)
	}
	workingLabel := ""
	if e.Context != nil {
		workingLabel = e.Context.WorkingDirectoryLabel(request.Config.WorkingContext, request.WorkingDir)
	}
	targetShell := adapter.PromptProfile().Shell
	if targetShell == "" {
		targetShell = string(adapter.ID())
	}
	response, err := provider.Translate(ctx, llm.TranslationRequest{Input: request.Input, Shell: targetShell, OS: runtime.GOOS, Architecture: runtime.GOARCH, WorkingContext: workingLabel})
	if err != nil {
		return Result{}, err
	}
	validator := e.Validator
	if validator == nil {
		validator = validate.Validator{}
	}
	if err := validator.Response(response); err != nil {
		return Result{}, err
	}
	switch response.Status {
	case "clarify":
		return Result{ExitCode: protocol.ExitClarify, Message: "humansh needs one more detail: " + response.Clarification + " Next: add that detail to your request, then try again."}, nil
	case "unsupported":
		return Result{
			ExitCode: protocol.ExitUnsupported,
			Message: fmt.Sprintf(
				"This request cannot be represented as one shell command. Next: edit the request and try again, or press %s to run it unchanged.",
				config.BindingLabel(request.Config.Shell.ForceLiteralBinding),
			),
		}, nil
	}
	// Validate the provider's exact bytes before shell-specific normalization.
	// Normalization must never erase a newline or terminal control and turn a
	// policy-rejected response into an accepted command.
	if err := validator.Command(response.Command); err != nil {
		return Result{}, err
	}
	command, err := adapter.NormalizeGenerated(response.Command)
	if err != nil {
		return Result{}, err
	}
	if command != response.Command {
		if err := validator.Command(command); err != nil {
			return Result{}, err
		}
	}
	if err := adapter.ValidateGenerated(ctx, command); err != nil {
		return Result{}, err
	}
	riskAnalyzer := e.Risk
	if riskAnalyzer == nil {
		riskAnalyzer = risk.Analyzer{}
	}
	riskResult := riskAnalyzer.Analyze(command)
	exit := protocol.ExitGeneratedLow
	if riskResult.Level == risk.Medium {
		exit = protocol.ExitGeneratedMedium
	} else if riskResult.Level == risk.High {
		exit = protocol.ExitGeneratedHigh
	}
	return Result{ExitCode: exit, Command: command, Explanation: response.Explanation, Risk: &riskResult}, nil
}

func (e Engine) Analyze(ctx context.Context, request RuntimeRequest) (Result, error) {
	adapter, ok := e.Shells.Get(request.ShellID)
	if !ok {
		return Result{}, fmt.Errorf("shell adapter %q unavailable", request.ShellID)
	}
	validator := e.Validator
	if validator == nil {
		validator = validate.Validator{}
	}
	if err := validator.Command(request.Input); err != nil {
		return Result{}, err
	}
	if err := adapter.ValidateGenerated(ctx, request.Input); err != nil {
		return Result{}, err
	}
	riskAnalyzer := e.Risk
	if riskAnalyzer == nil {
		riskAnalyzer = risk.Analyzer{}
	}
	riskResult := riskAnalyzer.Analyze(request.Input)
	exit := protocol.ExitGeneratedLow
	if riskResult.Level == risk.Medium {
		exit = protocol.ExitGeneratedMedium
	} else if riskResult.Level == risk.High {
		exit = protocol.ExitGeneratedHigh
	}
	return Result{ExitCode: exit, Command: request.Input, Risk: &riskResult}, nil
}
