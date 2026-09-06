package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/agenticlab-ai/humansh/internal/app"
	"github.com/agenticlab-ai/humansh/internal/bootstrap"
	"github.com/agenticlab-ai/humansh/internal/classifier"
	"github.com/agenticlab-ai/humansh/internal/config"
	usererr "github.com/agenticlab-ai/humansh/internal/errors"
	"github.com/agenticlab-ai/humansh/internal/llm"
	"github.com/agenticlab-ai/humansh/internal/shell"
	"github.com/agenticlab-ai/humansh/internal/shell/protocol"
	"github.com/agenticlab-ai/humansh/internal/version"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type IO struct {
	In       io.Reader
	Out, Err io.Writer
}

func Run(ctx context.Context, args []string, streams IO) int {
	if streams.In == nil {
		streams.In = os.Stdin
	}
	if streams.Out == nil {
		streams.Out = os.Stdout
	}
	if streams.Err == nil {
		streams.Err = os.Stderr
	}
	exitCode := 0
	root := newRootCommand(streams, &exitCode)
	root.SetArgs(args)
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintf(streams.Err, "humansh: %v\n", err)
		return 2
	}
	return exitCode
}

func newRootCommand(streams IO, exitCode *int) *cobra.Command {
	root := &cobra.Command{
		Use:           "humansh",
		Short:         "Natural-language commands for Zsh and Bash",
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          cobra.NoArgs,
		Run: func(command *cobra.Command, _ []string) {
			_ = command.Help()
		},
	}
	root.SetIn(streams.In)
	root.SetOut(streams.Out)
	root.SetErr(streams.Err)
	root.CompletionOptions.DisableDefaultCmd = true
	root.DisableAutoGenTag = true
	root.DisableSuggestions = true

	versionCommand := &cobra.Command{
		Use:                "version [--json]",
		Short:              "Print build and protocol version information",
		DisableFlagParsing: true,
		Run: func(command *cobra.Command, args []string) {
			if cobraHelpRequested(command, args, exitCode) {
				return
			}
			*exitCode = runVersion(args, streams)
		},
	}
	versionCommand.Flags().Bool("json", false, "emit JSON")
	root.AddCommand(versionCommand)

	addRuntimeCommand := func(name, use, short string, diagnostic bool, run func(context.Context, []string, bootstrap.Runtime, IO) int) *cobra.Command {
		command := &cobra.Command{
			Use:                use,
			Short:              short,
			DisableFlagParsing: true,
			Run: func(command *cobra.Command, args []string) {
				if cobraHelpRequested(command, args, exitCode) {
					return
				}
				if name == "provider" && renderProviderNestedHelp(args, streams.Out) {
					*exitCode = 0
					return
				}
				var runtime bootstrap.Runtime
				var err error
				if diagnostic {
					runtime, err = bootstrap.BuildDiagnostic()
				} else {
					runtime, err = bootstrap.Build()
				}
				if err != nil {
					*exitCode = renderError(streams, usererr.WithExit(protocol.ExitConfig, "config_load", "Configuration could not be loaded.", "Nothing was changed or executed.", false, err, usererr.Fix{Description: "Repair with", Command: "humansh doctor --fix"}), false)
					return
				}
				*exitCode = run(command.Context(), args, runtime, streams)
			},
		}
		command.Annotations = map[string]string{"humansh-command": name}
		root.AddCommand(command)
		return command
	}

	smartCommand := addRuntimeCommand("smart", "smart [protocol flags]", "Classify input and translate natural language", false, func(ctx context.Context, args []string, runtime bootstrap.Runtime, streams IO) int {
		return runProtocol(ctx, "smart", args, runtime, streams)
	})
	addProtocolFlags(smartCommand, true)
	translateCommand := addRuntimeCommand("translate", "translate [protocol flags]", "Force translation of input for review", false, func(ctx context.Context, args []string, runtime bootstrap.Runtime, streams IO) int {
		return runProtocol(ctx, "translate", args, runtime, streams)
	})
	addProtocolFlags(translateCommand, false)
	analyzeCommand := addRuntimeCommand("analyze", "analyze [--json]", "Validate and risk-score a shell command", false, runAnalyze)
	analyzeCommand.Flags().String("protocol", "", "shell protocol")
	analyzeCommand.Flags().String("shell", "zsh", "target shell")
	analyzeCommand.Flags().Bool("json", false, "emit JSON")
	classifyCommand := addRuntimeCommand("classify", "classify [--json]", "Inspect local classification evidence", false, func(ctx context.Context, args []string, runtime bootstrap.Runtime, streams IO) int {
		return runClassify(ctx, args, runtime, streams)
	})
	classifyCommand.Flags().String("shell", "zsh", "target shell")
	classifyCommand.Flags().String("first-token-kind", "unknown", "active-shell first token kind")
	classifyCommand.Flags().String("resolved-command-path", "", "exact external command path resolved by the active shell")
	classifyCommand.Flags().Bool("json", false, "emit JSON")
	classifyCommand.Flags().Bool("zle-status", false, "emit the fixed ZLE provider-status hint")
	addRuntimeCommand("classifier", "classifier [operation]", "Manage local classifier overrides", false, func(_ context.Context, args []string, runtime bootstrap.Runtime, streams IO) int {
		return runClassifier(args, runtime, streams)
	})
	providerCommand := addRuntimeCommand("provider", "provider <command>", "List, select, configure, or test providers", false, runProvider)
	providerCommand.SetHelpFunc(func(command *cobra.Command, _ []string) {
		printProviderHelp(command.OutOrStdout(), "")
	})
	addRuntimeCommand("config", "config [get|set|list]", "Read or update typed configuration", false, func(_ context.Context, args []string, runtime bootstrap.Runtime, streams IO) int {
		return runConfig(args, runtime, streams)
	})
	addRuntimeCommand("onboarding", "onboarding [zsh|bash]", "Learn the translate, review, and execute flow", false, runOnboarding)
	setupCommand := addRuntimeCommand("setup", "setup [flags]", "Configure Humansh with concise defaults", false, runSetup)
	setupCommand.Flags().Bool("yes", false, "use safe defaults without prompts")
	setupCommand.Flags().String("provider", "", "provider to select")
	setupCommand.Flags().String("shell", "", "integrate only bash or zsh")
	setupCommand.Flags().Bool("repair", false, "repair shell integration")
	setupCommand.Flags().Bool("no-shell-change", false, "do not edit shell startup files")
	setupCommand.Flags().Bool("advanced", false, "customize every setup preference")
	doctorCommand := addRuntimeCommand("doctor", "doctor [--fix] [--json]", "Diagnose or repair local setup", true, runDoctor)
	doctorCommand.Flags().String("provider", "", "diagnose one provider")
	doctorCommand.Flags().Bool("fix", false, "repair local deterministic setup")
	doctorCommand.Flags().Bool("json", false, "emit JSON")
	uninstallPurge := false
	uninstallYes := false
	uninstallCommand := &cobra.Command{
		Use:   "uninstall [--purge] [--yes]",
		Short: "Remove humansh while preserving configuration by default",
		Args:  cobra.NoArgs,
		Run: func(command *cobra.Command, _ []string) {
			*exitCode = runUninstall(command.Context(), uninstallPurge, uninstallYes, streams)
		},
	}
	uninstallCommand.Flags().BoolVar(&uninstallPurge, "purge", false, "also remove configuration and credentials after confirmation")
	uninstallCommand.Flags().BoolVar(&uninstallYes, "yes", false, "confirm --purge non-interactively")
	root.AddCommand(uninstallCommand)
	return root
}

func addProtocolFlags(command *cobra.Command, includeResolvedPath bool) {
	command.Flags().String("protocol", "", "shell protocol (defaults to the selected shell)")
	command.Flags().String("shell", "", "target shell: bash or zsh (defaults to configuration)")
	command.Flags().String("first-token-kind", "unknown", "active-shell first token kind")
	if includeResolvedPath {
		command.Flags().String("resolved-command-path", "", "exact external command path resolved by the active shell")
	}
}

func cobraHelpRequested(command *cobra.Command, args []string, exitCode *int) bool {
	if len(args) != 1 || args[0] != "--help" && args[0] != "-h" {
		return false
	}
	*exitCode = 0
	_ = command.Help()
	return true
}

func runProtocol(ctx context.Context, name string, args []string, rt bootstrap.Runtime, streams IO) int {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(streams.Err)
	protocolFlag := fs.String("protocol", "", "shell protocol")
	shellFlag := fs.String("shell", string(rt.Config.Shell.Name), "target shell")
	kindFlag := fs.String("first-token-kind", "unknown", "active-shell first token kind")
	resolvedPathFlag := fs.String("resolved-command-path", "", "resolved external command path")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return 2
	}
	shellID := shell.ID(*shellFlag)
	shellAdapter, ok := rt.Engine.Shells.Get(shellID)
	if !ok {
		fmt.Fprintf(streams.Err, "unsupported shell %q; humansh supports bash and zsh\n", *shellFlag)
		return 2
	}
	expectedProtocol := shellAdapter.SupportedProtocols()[0]
	if *protocolFlag == "" {
		*protocolFlag = expectedProtocol
	}
	if *protocolFlag != expectedProtocol {
		return renderError(streams, usererr.WithExit(protocol.ExitConfig, "protocol", "Unsupported shell protocol.", "Nothing was changed or executed.", false, nil), false)
	}
	kind := shell.FirstTokenKind(*kindFlag)
	if !kind.Valid() {
		fmt.Fprintf(streams.Err, "invalid --first-token-kind %q\n", *kindFlag)
		return 2
	}
	if !validResolvedCommandPath(*resolvedPathFlag) || *resolvedPathFlag != "" && kind != shell.TokenCommand {
		fmt.Fprintln(streams.Err, "invalid --resolved-command-path")
		return 2
	}
	if name != "smart" && *resolvedPathFlag != "" {
		fmt.Fprintln(streams.Err, "--resolved-command-path is only valid for smart classification")
		return 2
	}
	input, err := readProtocolInput(streams, name, 1<<20)
	if err != nil {
		return renderError(streams, usererr.WithExit(protocol.ExitConfig, "input", "Input could not be read.", "Nothing was changed or executed.", false, err), false)
	}
	cwd, _ := os.Getwd()
	request := app.RuntimeRequest{Input: string(input), ShellID: shellID, FirstTokenKind: kind, ResolvedCommandPath: *resolvedPathFlag, WorkingDir: cwd, Config: rt.Config, Overrides: rt.Overrides}
	var result app.Result
	if name == "smart" {
		result, err = rt.Engine.Smart(ctx, request)
	} else if name == "translate" {
		result, err = rt.Engine.Translate(ctx, request)
	}
	if err != nil {
		return renderError(streams, err, os.Getenv("HUMANSH_DEBUG") == "1")
	}
	if result.Command != "" && (result.ExitCode == 10 || result.ExitCode == 13 || result.ExitCode == 14) {
		fmt.Fprint(streams.Out, result.Command)
	}
	if result.Message != "" {
		fmt.Fprintln(streams.Err, result.Message)
	}
	return result.ExitCode
}

func runAnalyze(ctx context.Context, args []string, rt bootstrap.Runtime, streams IO) int {
	fs := flag.NewFlagSet("analyze", flag.ContinueOnError)
	fs.SetOutput(streams.Err)
	protocolFlag := fs.String("protocol", "", "shell protocol")
	shellFlag := fs.String("shell", string(rt.Config.Shell.Name), "target shell")
	jsonFlag := fs.Bool("json", false, "JSON output")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return 2
	}
	shellID := shell.ID(*shellFlag)
	shellAdapter, ok := rt.Engine.Shells.Get(shellID)
	if !ok {
		fmt.Fprintf(streams.Err, "unsupported shell %q; humansh supports bash and zsh\n", *shellFlag)
		return 2
	}
	expectedProtocol := shellAdapter.SupportedProtocols()[0]
	if *protocolFlag != "" && *protocolFlag != expectedProtocol {
		return renderError(streams, usererr.WithExit(protocol.ExitConfig, "protocol", "Unsupported shell protocol.", "Nothing was changed or executed.", false, nil), false)
	}
	input, err := readBounded(streams.In, 1<<20)
	if err != nil {
		return renderError(streams, usererr.WithExit(protocol.ExitConfig, "input", "Input could not be read.", "Nothing was changed or executed.", false, err), false)
	}
	result, err := rt.Engine.Analyze(ctx, app.RuntimeRequest{Input: string(input), ShellID: shellID, Config: rt.Config})
	if err != nil {
		return renderError(streams, err, os.Getenv("HUMANSH_DEBUG") == "1")
	}
	if *jsonFlag {
		data, _ := json.MarshalIndent(map[string]any{"syntax_valid": true, "risk": result.Risk, "exit_code": result.ExitCode}, "", "  ")
		fmt.Fprintln(streams.Out, string(data))
	} else if *protocolFlag == "" {
		fmt.Fprintln(streams.Out, "Syntax: valid")
		fmt.Fprintf(streams.Out, "Risk: %s\n", result.Risk.Level)
		for _, reason := range result.Risk.Reasons {
			fmt.Fprintf(streams.Out, "  - %s\n", reason)
		}
	}
	return result.ExitCode
}

func runClassify(ctx context.Context, args []string, rt bootstrap.Runtime, streams IO) int {
	fs := flag.NewFlagSet("classify", flag.ContinueOnError)
	fs.SetOutput(streams.Err)
	shellFlag := fs.String("shell", string(rt.Config.Shell.Name), "shell")
	kindFlag := fs.String("first-token-kind", "unknown", "first token kind")
	resolvedPathFlag := fs.String("resolved-command-path", "", "resolved external command path")
	jsonFlag := fs.Bool("json", false, "JSON output")
	zleStatus := fs.Bool("zle-status", false, "emit the fixed ZLE provider-status hint")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return 2
	}
	if _, ok := rt.Engine.Shells.Get(shell.ID(*shellFlag)); !ok {
		fmt.Fprintf(streams.Err, "unsupported shell %q; humansh supports bash and zsh\n", *shellFlag)
		return 2
	}
	input, err := readBounded(streams.In, 1<<20)
	if err != nil {
		fmt.Fprintln(streams.Err, err)
		return 2
	}
	kind := shell.FirstTokenKind(*kindFlag)
	if !kind.Valid() {
		fmt.Fprintf(streams.Err, "invalid --first-token-kind %q\n", *kindFlag)
		return 2
	}
	if !validResolvedCommandPath(*resolvedPathFlag) || *resolvedPathFlag != "" && kind != shell.TokenCommand {
		fmt.Fprintln(streams.Err, "invalid --resolved-command-path")
		return 2
	}
	result := rt.Engine.Classifier.ClassifyContext(ctx, classifier.Input{Raw: string(input), Shell: *shellFlag, FirstTokenKind: kind, ResolvedCommandPath: *resolvedPathFlag, Overrides: rt.Overrides})
	if ctx.Err() != nil {
		return 130
	}
	if *zleStatus {
		// Return the complete fixed decision so ZLE never has to classify the same
		// input a second time. The provider label rides only with translation.
		switch result.Outcome {
		case classifier.Literal:
			fmt.Fprint(streams.Out, "literal")
		case classifier.Ambiguous:
			fmt.Fprint(streams.Out, "ambiguous")
		case classifier.Natural:
			fmt.Fprintf(streams.Out, "translate %s", rt.Config.Provider.Label())
		}
		return 0
	}
	if *jsonFlag {
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Fprintln(streams.Out, string(data))
		return 0
	}
	renderClassification(streams.Out, result)
	return 0
}

func renderClassification(out io.Writer, result classifier.Result) {
	fmt.Fprintf(out, "Classification: %s\nCommand score: %d\nEnglish score: %d\nDecision: %s\n", result.Outcome, result.CommandScore, result.EnglishScore, result.DecisionCode)
	for _, e := range result.Evidence {
		if e.Domain != classifier.DecisionEvidence {
			fmt.Fprintf(out, "  %+d %s/%s — %s\n", e.Weight, e.Domain, e.Code, e.Detail)
		}
	}
	fmt.Fprintln(out, "The typed line was not executed and no AI provider was contacted.")
}

func validResolvedCommandPath(value string) bool {
	return value == "" || filepath.IsAbs(value) && !strings.ContainsAny(value, "\x00\r\n")
}

func runClassifier(args []string, rt bootstrap.Runtime, streams IO) int {
	if len(args) == 0 || (len(args) == 1 && args[0] == "list") {
		data, _ := json.MarshalIndent(map[string]any{
			"overrides":  rt.Overrides,
			"thresholds": map[string]int{"strong_evidence_min": 5, "weak_evidence_max": 2},
		}, "", "  ")
		fmt.Fprintln(streams.Out, string(data))
		return 0
	}
	if len(args) != 1 {
		fmt.Fprintln(streams.Err, "usage: humansh classifier add-command|remove-command|add-english-prefix|remove-english-prefix < value")
		return 2
	}
	if args[0] == "list" {
		fmt.Fprintln(streams.Err, "usage: humansh classifier list")
		return 2
	}
	value, err := readOverrideValue(streams, args[0])
	if err != nil {
		fmt.Fprintln(streams.Err, err)
		return 2
	}
	o := rt.Overrides
	switch args[0] {
	case "add-command":
		o.AlwaysCommands = appendUnique(o.AlwaysCommands, value)
	case "remove-command":
		o.AlwaysCommands = remove(o.AlwaysCommands, value)
	case "add-english-prefix":
		o.AlwaysNaturalLanguagePrefixes = appendUnique(o.AlwaysNaturalLanguagePrefixes, value)
	case "remove-english-prefix":
		o.AlwaysNaturalLanguagePrefixes = remove(o.AlwaysNaturalLanguagePrefixes, value)
	default:
		fmt.Fprintln(streams.Err, "unknown classifier operation")
		return 2
	}
	if conflicts := config.OverrideConflicts(o); len(conflicts) > 0 {
		for _, conflict := range conflicts {
			fmt.Fprintln(streams.Err, conflict)
		}
		return protocol.ExitConfig
	}
	if err := rt.Store.SaveOverridesAtomic(o); err != nil {
		fmt.Fprintln(streams.Err, err)
		return protocol.ExitConfig
	}
	return 0
}

func readOverrideValue(streams IO, operation string) (string, error) {
	interactive := readerIsTerminal(streams.In)
	if interactive {
		prompt := "Command name: "
		if strings.Contains(operation, "english-prefix") {
			prompt = "English prefix: "
		}
		fmt.Fprint(streams.Out, prompt)
	}
	data, err := readMaybeInteractiveLine(streams.In, 4096, interactive)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(data))
	if value == "" {
		return "", fmt.Errorf("override value cannot be empty")
	}
	return value, nil
}

func readProtocolInput(streams IO, operation string, limit int64) ([]byte, error) {
	interactive := readerIsTerminal(streams.In)
	if interactive {
		fmt.Fprintf(streams.Err, "%s request: ", operation)
	}
	return readMaybeInteractiveLine(streams.In, limit, interactive)
}

func readMaybeInteractiveLine(reader io.Reader, limit int64, interactive bool) ([]byte, error) {
	if !interactive {
		return readBounded(reader, limit)
	}
	line, err := bufio.NewReader(io.LimitReader(reader, limit+1)).ReadString('\n')
	if err != nil && err != io.EOF {
		return nil, err
	}
	if int64(len(line)) > limit {
		return nil, fmt.Errorf("input exceeds %d bytes", limit)
	}
	return []byte(strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")), nil
}

func runProvider(ctx context.Context, args []string, rt bootstrap.Runtime, streams IO) int {
	if len(args) == 0 {
		printProviderHelp(streams.Out, rt.Config.Provider)
		return 0
	}
	switch args[0] {
	case "help":
		if len(args) == 1 {
			printProviderHelp(streams.Out, rt.Config.Provider)
			return 0
		}
		if len(args) == 2 && validProviderHelpCommand(args[1]) {
			printProviderCommandHelp(streams.Out, args[1])
			return 0
		}
		fmt.Fprintln(streams.Err, "Usage: humansh provider help [list|use|select|configure|test]")
		return 2
	case "list":
		return runProviderList(ctx, args[1:], rt, streams)
	case "use", "select":
		if providerHelpRequested(args[1:]) {
			printProviderCommandHelp(streams.Out, args[0])
			return 0
		}
		if len(args) != 2 {
			fmt.Fprintln(streams.Err, "Usage: humansh provider use <codex|claude|cursor|openrouter>")
			return 2
		}
		id := llm.ProviderID(args[1])
		provider, ok := rt.Engine.Providers.Get(id)
		if !ok {
			fmt.Fprintf(streams.Err, "Unknown provider %q. Choose codex, claude, cursor, or openrouter.\n", args[1])
			return 2
		}
		fmt.Fprintln(streams.Err, "Warning: this readiness check sends one minimal prompt and may consume provider quota.")
		diagnostic := provider.Probe(ctx)
		if !diagnostic.Available {
			printUnavailableProvider(streams.Err, id, diagnostic)
			return protocol.ExitProviderUnavailable
		}
		cfg := rt.Config
		cfg.Provider = id
		targetShells := installedShellIDs(rt.Paths, cfg.Shell.Name)
		// Re-render the managed block as well, so the provider label it exports
		// matches the new selection in shells started from now on.
		if err := rt.Store.SaveAndApply(cfg, func() error {
			_, setupErr := config.SetupWithOptions(rt.Paths, cfg, version.Version, config.SetupOptions{Shells: targetShells})
			return setupErr
		}); err != nil {
			fmt.Fprintln(streams.Err, err)
			return protocol.ExitConfig
		}
		fmt.Fprintf(streams.Out, "✓ Active provider: %s (%s)\n", cfg.Provider.Label(), cfg.Provider)
		fmt.Fprintln(streams.Out, "Next: run `humansh provider test` to verify a real translation.")
		return 0
	case "test":
		if providerHelpRequested(args[1:]) {
			printProviderCommandHelp(streams.Out, "test")
			return 0
		}
		if len(args) > 2 {
			fmt.Fprintln(streams.Err, "Usage: humansh provider test [codex|claude|cursor|openrouter]")
			return 2
		}
		id := rt.Config.Provider
		if len(args) > 1 {
			id = llm.ProviderID(args[1])
		}
		_, ok := rt.Engine.Providers.Get(id)
		if !ok {
			fmt.Fprintf(streams.Err, "Unknown provider %q. Choose codex, claude, cursor, or openrouter.\n", id)
			return protocol.ExitProviderUnavailable
		}
		fmt.Fprintln(streams.Err, "Warning: this real translation test may consume provider quota or OpenRouter credits.")
		cfg := rt.Config
		cfg.Provider = id
		cwd, _ := os.Getwd()
		result, err := rt.Engine.Translate(ctx, app.RuntimeRequest{Input: "print the current working directory", ShellID: cfg.Shell.Name, WorkingDir: cwd, Config: cfg, Overrides: rt.Overrides})
		if err != nil {
			return renderError(streams, err, os.Getenv("HUMANSH_DEBUG") == "1")
		}
		d := llm.Diagnostic{
			Installed: true, Configured: true, Authenticated: true, Available: true, LiveCheck: true,
			AuthMode: "provider_managed", Capabilities: []string{"structured-translation"},
			Message: id.Label() + " completed a structured translation",
		}
		if id == llm.OpenRouter {
			d.AuthMode = "api_key"
			d.Version = cfg.OpenRouter.Model
		}
		data, _ := json.MarshalIndent(map[string]any{"provider": id, "diagnostic": d, "translation": result}, "", "  ")
		fmt.Fprintln(streams.Out, string(data))
		return 0
	case "configure":
		if providerHelpRequested(args[1:]) {
			printProviderCommandHelp(streams.Out, "configure")
			return 0
		}
		if len(args) < 2 {
			printProviderCommandHelp(streams.Out, "configure")
			return 0
		}
		if providerHelpRequested(args[2:]) {
			printProviderCommandHelp(streams.Out, "configure")
			return 0
		}
		switch args[1] {
		case "openrouter":
			return configureOpenRouter(ctx, args[2:], rt, streams)
		case "codex":
			return configureCodexConfirmation(ctx, args[2:], rt, streams)
		case "claude":
			if len(args) != 2 {
				return 2
			}
			fmt.Fprintln(streams.Out, "Claude Code authentication is managed by the selected CLI distribution; Humansh does not invoke or inspect its login subcommands.")
			fmt.Fprintln(streams.Out, "Next: run `humansh provider test claude` to verify a real structured translation.")
			return 0
		case "cursor":
			if len(args) != 2 {
				return 2
			}
			fmt.Fprintln(streams.Out, "Cursor authentication is managed by the selected CLI distribution; Humansh does not invoke or inspect its login subcommands.")
			fmt.Fprintln(streams.Out, "Next: run `humansh provider test cursor` to verify a real structured translation.")
			return 0
		default:
			fmt.Fprintf(streams.Err, "Unknown provider %q. Choose codex, claude, cursor, or openrouter.\n", args[1])
			fmt.Fprintln(streams.Err, "Next: run `humansh provider help configure` for examples.")
			return 2
		}
	default:
		fmt.Fprintf(streams.Err, "Unknown provider command %q.\n", args[0])
		fmt.Fprintln(streams.Err, "Next: run `humansh provider help` to see the available commands.")
		return 2
	}
}

func configureCodexConfirmation(ctx context.Context, args []string, rt bootstrap.Runtime, streams IO) int {
	fs := flag.NewFlagSet("provider configure codex", flag.ContinueOnError)
	fs.SetOutput(streams.Err)
	fs.Bool("confirm-subscription-auth", false, "deprecated compatibility option; provider authentication is managed by the CLI")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return 2
	}
	_ = ctx
	_ = rt
	fmt.Fprintln(streams.Out, "Codex authentication is managed by the selected Codex CLI distribution; Humansh does not invoke or inspect its login/status subcommands.")
	fmt.Fprintln(streams.Out, "Next: run `humansh provider test codex` to verify a real structured translation.")
	return 0
}

func configureOpenRouter(ctx context.Context, args []string, rt bootstrap.Runtime, streams IO) int {
	fs := flag.NewFlagSet("provider configure openrouter", flag.ContinueOnError)
	fs.SetOutput(streams.Err)
	model := fs.String("model", rt.Config.OpenRouter.Model, "concrete OpenRouter model slug")
	fs.Bool("yes", false, "deprecated; the required compatibility check now runs automatically")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return 2
	}
	if *model == "" || *model == "openrouter/auto" {
		fmt.Fprintln(streams.Err, "A concrete OpenRouter model slug is required; openrouter/auto is not accepted.")
		return 2
	}
	key, err := config.LoadOpenRouterKey(rt.Paths)
	if err == nil && key == "" {
		err = config.ConfigureOpenRouterKey(rt.Paths, streams.In, streams.Out, streams.Err)
		if err == nil {
			key, err = config.LoadOpenRouterKey(rt.Paths)
		}
	}
	if err != nil || key == "" {
		fmt.Fprintln(streams.Err, "OpenRouter key could not be loaded after configuration.")
		return protocol.ExitProviderAuth
	}
	if rt.ProviderSetup == nil {
		return renderError(streams, usererr.WithExit(protocol.ExitInternal, "provider_setup", "Provider setup is unavailable.", "Nothing was changed or executed.", false, nil), false)
	}
	if err := rt.ProviderSetup.ValidateOpenRouterKey(ctx, rt.Config, *model, key); err != nil {
		return renderError(streams, err, false)
	}
	fmt.Fprintf(streams.Out, "Checking whether %s supports strict structured output without using model credits.\n", *model)
	if err := rt.ProviderSetup.ValidateOpenRouterModel(ctx, rt.Config, *model, key); err != nil {
		return renderError(streams, err, false)
	}
	fmt.Fprintf(streams.Out, "Running the required minimal compatibility check with %s (one small metered request).\n", *model)
	_, err = rt.ProviderSetup.ProbeOpenRouter(ctx, rt.Config, *model, key)
	if err != nil {
		return renderError(streams, err, false)
	}
	cfg := rt.Config
	cfg.OpenRouter.Model = *model
	cfg.OpenRouter.StructuredOutputProven = true
	cfg.OpenRouter.StructuredOutputModel = *model
	if err := rt.Store.SaveAtomic(cfg); err != nil {
		fmt.Fprintln(streams.Err, err)
		return protocol.ExitConfig
	}
	fmt.Fprintf(streams.Out, "OpenRouter model %s passed the strict-schema probe and was saved.\n", *model)
	return 0
}

func runConfig(args []string, rt bootstrap.Runtime, streams IO) int {
	if len(args) == 0 || args[0] == "list" {
		// List the same keys `config get` and `config set` accept, so a key read
		// here can be fed straight back to them. Dumping the runtime struct would
		// print Go field names and raw nanosecond durations that neither command
		// understands. Secrets are excluded by construction: configGet exposes no
		// credential values.
		values := make(map[string]string, len(configKeys))
		for _, key := range configKeys {
			if value, ok := configGet(rt.Config, key); ok {
				values[key] = value
			}
		}
		data, _ := json.MarshalIndent(values, "", "  ")
		fmt.Fprintln(streams.Out, string(data))
		return 0
	}
	if args[0] == "get" && len(args) == 2 {
		value, ok := configGet(rt.Config, args[1])
		if !ok {
			fmt.Fprintf(streams.Err, "humansh: %q is not a supported configuration key.\nNothing was changed.\nFix: use one of: %s\nCheck: `humansh config list`\n", args[1], strings.Join(configKeys, ", "))
			return 2
		}
		fmt.Fprintln(streams.Out, value)
		return 0
	}
	if args[0] == "set" && len(args) >= 3 {
		cfg := rt.Config
		if err := configSet(&cfg, args[1], strings.Join(args[2:], " ")); err != nil {
			fmt.Fprintln(streams.Err, err)
			return 2
		}
		if strings.HasPrefix(args[1], "shell.") {
			targetShells := installedShellIDs(rt.Paths, cfg.Shell.Name)
			if err := rt.Store.SaveAndApply(cfg, func() error {
				_, setupErr := config.SetupWithOptions(rt.Paths, cfg, version.Version, config.SetupOptions{Shells: targetShells})
				return setupErr
			}); err != nil {
				fmt.Fprintln(streams.Err, err)
				return protocol.ExitConfig
			}
			fmt.Fprintln(streams.Out, "Shell setting saved. Open a new terminal to load it in every configured shell.")
		} else if err := rt.Store.SaveAtomic(cfg); err != nil {
			fmt.Fprintln(streams.Err, err)
			return protocol.ExitConfig
		}
		return 0
	}
	return 2
}

type setupOptions struct {
	yes           bool
	providerName  string
	shellName     string
	repair        bool
	noShellChange bool
	advanced      bool
}

func parseSetupOptions(args []string, streams IO) (setupOptions, bool) {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(streams.Err)
	yes := fs.Bool("yes", false, "use safe defaults without prompts")
	providerName := fs.String("provider", "", "provider to select")
	shellName := fs.String("shell", "", "restrict integration to bash or zsh")
	repair := fs.Bool("repair", false, "repair shell integration")
	noShellChange := fs.Bool("no-shell-change", false, "do not edit shell activation files")
	advanced := fs.Bool("advanced", false, "customize every setup preference")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return setupOptions{}, false
	}
	if *repair && (*providerName != "" || *shellName != "") {
		fmt.Fprintln(streams.Err, "--repair preserves provider and installed shell integrations and cannot be combined with --provider or --shell")
		return setupOptions{}, false
	}
	normalizedShell := strings.ToLower(*shellName)
	if normalizedShell != "" && normalizedShell != string(shell.Zsh) && normalizedShell != string(shell.Bash) {
		fmt.Fprintf(streams.Err, "unsupported shell %q; choose bash or zsh\n", *shellName)
		return setupOptions{}, false
	}
	return setupOptions{
		yes:           *yes,
		providerName:  *providerName,
		shellName:     normalizedShell,
		repair:        *repair,
		noShellChange: *noShellChange,
		advanced:      *advanced,
	}, true
}

func runSetup(ctx context.Context, args []string, rt bootstrap.Runtime, streams IO) int {
	options, ok := parseSetupOptions(args, streams)
	if !ok {
		return 2
	}
	if options.repair {
		hasConfig, err := setupPathExists(rt.Paths.ConfigFile)
		if err != nil {
			fmt.Fprintf(streams.Err, "humansh: cannot inspect the existing configuration: %v\nNothing was changed or executed.\n", err)
			return protocol.ExitConfig
		}
		if !hasConfig {
			fmt.Fprintln(streams.Err, "humansh: there is no existing configuration to repair.\nNothing was changed or executed.\nNext: run `humansh setup`.")
			return protocol.ExitConfig
		}
	}
	if options.advanced {
		return runAdvancedSetup(ctx, options, rt, streams)
	}
	return runQuickSetup(ctx, options, rt, streams)
}

func runAdvancedSetup(ctx context.Context, options setupOptions, rt bootstrap.Runtime, streams IO) int {
	interactive := readerIsTerminal(streams.In) && !options.yes && !options.repair
	ui := newSetupUI(streams, interactive)
	ui.ctx = ctx
	ui.header()
	ui.section(1, 6, "Shell compatibility")
	ui.note("Humansh automatically configures every compatible Zsh or Bash installation it finds.")
	cfg := rt.Config
	var requestedShell shell.ID
	if options.shellName != "" {
		requestedShell = shell.ID(options.shellName)
		switch requestedShell {
		case shell.Zsh:
		case shell.Bash:
		default:
			fmt.Fprintf(streams.Err, "unsupported shell %q; choose bash or zsh\n", options.shellName)
			return 2
		}
	}
	providerReady := false
	candidates := []shell.ID{shell.Zsh, shell.Bash}
	if requestedShell != "" {
		candidates = []shell.ID{requestedShell}
	} else if options.repair {
		if installed, stateErr := config.LoadInstallState(rt.Paths.InstallState); stateErr == nil {
			candidates = installed.ShellIDs()
		}
	}
	shellDiagnostics := make(map[shell.ID]shell.Diagnostic, len(candidates))
	ui.withLoader("Checking installed shells…", func() {
		for _, id := range candidates {
			if shellAdapter, ok := rt.Engine.Shells.Get(id); ok {
				shellDiagnostics[id] = shellAdapter.Diagnose(ctx)
			}
		}
	})
	if ctx.Err() != nil {
		printSetupCancellation(streams.Out, false)
		return 130
	}
	var targetShells []shell.ID
	for _, id := range candidates {
		_, ok := rt.Engine.Shells.Get(id)
		if !ok {
			if requestedShell != "" {
				fmt.Fprintf(streams.Err, "humansh: %s integration is unavailable in this build.\nNothing was changed or executed.\n", shellDisplayName(id))
				return protocol.ExitConfig
			}
			continue
		}
		diagnostic := shellDiagnostics[id]
		if diagnostic.Available {
			targetShells = append(targetShells, id)
			ui.status(true, setupShellVersionLabel(id, diagnostic.Version), "compatible")
			continue
		}
		if options.repair {
			targetShells = append(targetShells, id)
			ui.status(false, setupShellVersionLabel(id, diagnostic.Version), setupShellRequirement(id)+"; activation files will still be repaired")
			continue
		}
		if requestedShell != "" {
			fmt.Fprintf(streams.Err, "humansh: %s is unavailable: %s.\nNothing was changed or executed.\nFix: install %s, ensure `%s --version` works, then rerun `humansh setup`.\n", shellDisplayName(id), diagnostic.Message, shellDisplayName(id), id)
			return protocol.ExitConfig
		}
		if diagnostic.Installed {
			ui.status(false, setupShellVersionLabel(id, diagnostic.Version), setupShellRequirement(id))
		}
	}
	if len(targetShells) == 0 {
		fmt.Fprintln(streams.Err, "humansh: no supported interactive shell is available.\nNothing was changed or executed.\nFix: install Zsh or Bash 4.3+, then rerun `humansh setup`.")
		return protocol.ExitConfig
	}
	cfg.Shell.Name = targetShells[0]
	if cfg.Shell.Name == shell.Zsh {
		cfg.Shell.Protocol = protocol.Version
	} else {
		cfg.Shell.Protocol = protocol.ReadlineVersion
		cfg.Shell.SmartEnter = false
	}
	if options.noShellChange {
		previousStartups, previewErr := config.PreviewRemovedStartupChanges(rt.Paths, targetShells, options.repair)
		if previewErr != nil {
			fmt.Fprintf(streams.Err, "humansh: cannot inspect installed shell integrations: %v\nNothing was changed or executed.\n", previewErr)
			return protocol.ExitConfig
		}
		if len(previousStartups) > 0 {
			fmt.Fprintln(streams.Err, "humansh: --no-shell-change cannot restrict existing shell integrations because prior startup blocks would remain active.\nNothing was changed or executed.")
			return protocol.ExitConfig
		}
	}

	ui.section(2, 6, "AI provider")
	var pendingOpenRouter *setupOpenRouterCredential
	if !options.repair {
		selected, ok, code := configureSetupProvider(ctx, &rt, &cfg, options.providerName, options.yes, ui, &pendingOpenRouter)
		if code == 130 || ctx.Err() != nil {
			printSetupCancellation(streams.Out, pendingOpenRouter != nil)
			return 130
		}
		if code != 0 {
			return code
		}
		if !ok {
			_, _, requiredCode := setupProviderRequired(ui)
			return requiredCode
		}
		providerReady = true
		cfg.Provider = selected
	} else {
		provider, exists := rt.Engine.Providers.Get(cfg.Provider)
		if !exists {
			ui.warning("The configured provider is unavailable in this build.")
			_, _, requiredCode := setupProviderRequired(ui)
			return requiredCode
		}
		var diagnostic llm.Diagnostic
		ui.note("The live check sends one constant minimal prompt and may consume a small amount of provider quota.")
		ui.withLoader("Checking "+setupProviderName(cfg.Provider)+"…", func() {
			diagnostic = provider.Probe(ctx)
		})
		if ctx.Err() != nil {
			printSetupCancellation(streams.Out, false)
			return 130
		}
		if !diagnostic.Available {
			ui.providerProblem(cfg.Provider, diagnostic)
			ui.providerRecovery(cfg.Provider, diagnostic)
			_, _, requiredCode := setupProviderRequired(ui)
			return requiredCode
		}
		providerReady = true
		ui.status(true, setupProviderName(cfg.Provider), "provider configuration preserved during repair")
	}

	ui.section(3, 6, "Translation preferences")
	if err := configureSetupTranslation(rt.Paths, &cfg, providerReady, ui); err != nil {
		printSetupCancellation(streams.Out, pendingOpenRouter != nil)
		return 130
	}

	ui.section(4, 6, "Shell controls")
	if err := configureSetupShell(&cfg, targetShells, ui); err != nil {
		printSetupCancellation(streams.Out, pendingOpenRouter != nil)
		return 130
	}

	ui.section(5, 6, "Review")
	effectiveNoShellChange := options.noShellChange
	var reviewedStartups []config.StartupChange
	reviewedRemovals, migrationPreviewErr := config.PreviewRemovedStartupChanges(rt.Paths, targetShells, options.repair)
	if migrationPreviewErr != nil {
		fmt.Fprintf(streams.Err, "humansh: cannot prepare shell-integration changes: %v\nNo humansh configuration or shell files were changed.\n", migrationPreviewErr)
		return protocol.ExitConfig
	}
	if len(reviewedRemovals) > 0 && effectiveNoShellChange {
		fmt.Fprintln(streams.Err, "humansh: --no-shell-change cannot restrict existing shell integrations because prior startup blocks would remain active.\nNo humansh configuration or shell files were changed.")
		return protocol.ExitConfig
	}
	if !effectiveNoShellChange {
		changes, previewErr := config.PreviewStartupChanges(rt.Paths, cfg, targetShells, options.repair)
		if previewErr != nil {
			if config.IsStartupAccessError(previewErr) && ui.interactive {
				ui.warning("Shell startup cannot be updated automatically: " + previewErr.Error())
				manual, promptErr := ui.askYesNo("Continue without editing shell startup files?", true)
				if promptErr != nil || !manual {
					printSetupCancellation(streams.Out, pendingOpenRouter != nil)
					return 130
				}
				effectiveNoShellChange = true
				ui.note("The exact block to add manually will be printed after setup.")
			} else {
				fmt.Fprintf(streams.Err, "humansh: cannot prepare shell startup patches: %v\nNo humansh configuration or shell files were changed.\n", previewErr)
				if config.IsStartupAccessError(previewErr) {
					fmt.Fprintln(streams.Err, "Next: rerun `humansh setup --no-shell-change` to finish setup and print the exact blocks to add manually.")
				}
				return protocol.ExitConfig
			}
		} else {
			reviewedStartups = changes
		}
	}
	if len(reviewedRemovals) > 0 && effectiveNoShellChange {
		fmt.Fprintln(streams.Err, "humansh: cannot restrict shell integrations without removing prior managed startup blocks.\nNo humansh configuration or shell files were changed.")
		return protocol.ExitConfig
	}
	printSetupReview(cfg, targetShells, providerReady, effectiveNoShellChange, pendingOpenRouter != nil && pendingOpenRouter.key != "", ui)
	if len(reviewedRemovals) > 0 {
		ui.note("The following prior shell startup blocks will be removed:")
		for _, removal := range reviewedRemovals {
			ui.startupPatch(removal)
		}
	}
	if len(reviewedStartups) > 0 {
		for _, startup := range reviewedStartups {
			ui.startupPatch(startup)
		}
	} else if effectiveNoShellChange {
		ui.note("No shell startup file will be edited; manual blocks will be printed after setup.")
	}
	if ui.interactive {
		apply, err := ui.askYesNo("Apply this setup?", true)
		if err != nil || !apply {
			printSetupCancellation(streams.Out, pendingOpenRouter != nil)
			return 130
		}
	}
	if ctx.Err() != nil {
		printSetupCancellation(streams.Out, pendingOpenRouter != nil)
		return 130
	}

	applySetup := func() (setupErr error) {
		credentialStored := false
		if pendingOpenRouter != nil && pendingOpenRouter.key != "" {
			storage, persistErr := config.PersistOpenRouterKey(rt.Paths, pendingOpenRouter.key, true, streams.Err)
			if persistErr != nil {
				return fmt.Errorf("save OpenRouter API key: %w", persistErr)
			}
			pendingOpenRouter.storage = storage
			credentialStored = true
		}
		defer func() {
			if setupErr == nil || !credentialStored {
				return
			}
			if rollbackErr := config.DeleteOpenRouterKey(rt.Paths); rollbackErr != nil {
				setupErr = errors.Join(setupErr, fmt.Errorf("roll back OpenRouter API key: %w", rollbackErr))
			}
		}()
		_, setupErr = config.SetupWithOptions(rt.Paths, cfg, version.Version, config.SetupOptions{NoShellChange: effectiveNoShellChange, Repair: options.repair, Shells: targetShells, ReviewedStartups: reviewedStartups, ReviewedRemovals: reviewedRemovals})
		return setupErr
	}
	var err error
	if options.repair {
		err = applySetup()
	} else {
		err = rt.Store.SaveAndApply(cfg, applySetup)
	}
	if err != nil {
		fmt.Fprintln(streams.Err, err)
		if config.IsStartupAccessError(err) {
			fmt.Fprintln(streams.Err, "Next: rerun `humansh setup --no-shell-change` to finish setup and print the exact blocks to add manually.")
		}
		return protocol.ExitConfig
	}

	ui.section(6, 6, "Complete")
	if pendingOpenRouter != nil && pendingOpenRouter.storage != "" {
		ui.success("OpenRouter API key saved to " + pendingOpenRouter.storage + ".")
	}
	if effectiveNoShellChange {
		ui.success("Configuration saved; shell startup was not changed.")
		for _, id := range targetShells {
			fmt.Fprintf(streams.Out, "\nAdd this exact block to ~/%s:\n", shellStartupName(id))
			fmt.Fprint(streams.Out, config.ManagedBlockForShell(rt.Paths, cfg, id))
		}
	} else {
		ui.success("humansh setup complete.")
		fmt.Fprintln(streams.Out, "  Next: open a new terminal. Humansh will load automatically in each configured shell.")
		for _, id := range targetShells {
			switch id {
			case shell.Zsh:
				fmt.Fprintf(streams.Out, "  Zsh: Enter detects natural language; %s forces translation.\n", config.BindingLabel(cfg.Shell.ForceTranslateBinding))
			case shell.Bash:
				fmt.Fprintf(streams.Out, "  Bash: type natural language and press %s; Enter runs normal Bash commands.\n", config.BindingLabel(cfg.Shell.ForceTranslateBinding))
			}
		}
		fmt.Fprintln(streams.Out, "  Check: run humansh-bindings in any configured shell to inspect its active shortcuts.")
	}
	return 0
}

type setupOpenRouterCredential struct {
	key     string
	storage string
}

func shellDisplayName(id shell.ID) string {
	if id == shell.Bash {
		return "Bash"
	}
	return "Zsh"
}

func shellStartupName(id shell.ID) string {
	if id == shell.Bash {
		return ".bashrc"
	}
	return ".zshrc"
}

func shellNames(ids []shell.ID) string {
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		names = append(names, shellDisplayName(id))
	}
	return strings.Join(names, " and ")
}

func setupShellVersionLabel(id shell.ID, rawVersion string) string {
	name := shellDisplayName(id)
	version := ""
	switch id {
	case shell.Zsh:
		fields := strings.Fields(rawVersion)
		if len(fields) >= 2 && strings.EqualFold(fields[0], "zsh") {
			version = fields[1]
		}
	case shell.Bash:
		lower := strings.ToLower(rawVersion)
		if start := strings.Index(lower, "version "); start >= 0 {
			candidate := rawVersion[start+len("version "):]
			end := 0
			for end < len(candidate) && (candidate[end] == '.' || candidate[end] >= '0' && candidate[end] <= '9') {
				end++
			}
			version = candidate[:end]
		}
	}
	if version == "" {
		return name + " (version unavailable)"
	}
	return name + " " + version
}

func setupShellRequirement(id shell.ID) string {
	if id == shell.Bash {
		return "minimum required: Bash 4.3"
	}
	return "requires a working ZLE-capable Zsh"
}

func installedShellIDs(paths config.Paths, fallback shell.ID) []shell.ID {
	if state, err := config.LoadInstallState(paths.InstallState); err == nil {
		if ids := state.ShellIDs(); len(ids) > 0 {
			return ids
		}
	}
	return []shell.ID{fallback}
}

const setupChooseDifferentProvider = -1

func printSetupCancellation(out io.Writer, openRouterProbeRan bool) {
	if openRouterProbeRan {
		fmt.Fprintln(out, "\nSetup cancelled. The OpenRouter compatibility check ran, but no credential, configuration, or shell file was changed.")
		return
	}
	fmt.Fprintln(out, "\nSetup cancelled. Nothing was changed.")
}

func selectSetupProvider(ctx context.Context, rt bootstrap.Runtime, explicit string, yes, interactive bool, streams IO) (llm.ProviderID, bool, int) {
	ui := newSetupUI(streams, interactive && !yes)
	ui.ctx = ctx
	var diagnostics map[llm.ProviderID]llm.Diagnostic
	ui.withLoader("Checking AI providers…", func() {
		diagnostics = diagnoseSetupProviders(ctx, rt)
	})
	if ctx.Err() != nil {
		return "", false, 130
	}
	selected, ok, code := chooseSetupProvider(rt.Config.Provider, explicit, yes, ui, diagnostics)
	if code != 0 || !ok {
		return "", false, code
	}
	ready, _, code := activateSetupProvider(ctx, rt, selected, diagnostics[selected], true, ui)
	return selected, ready, code
}

func configureSetupProvider(ctx context.Context, rt *bootstrap.Runtime, cfg *config.RuntimeConfig, explicit string, yes bool, ui *setupUI, pendingOpenRouter **setupOpenRouterCredential) (llm.ProviderID, bool, int) {
	var diagnostics map[llm.ProviderID]llm.Diagnostic
	ui.withLoader("Checking AI providers…", func() {
		diagnostics = diagnoseSetupProviders(ctx, *rt)
	})
	if ctx.Err() != nil {
		return "", false, 130
	}
	current := cfg.Provider
	for {
		selected, ok, code := chooseSetupProvider(current, explicit, yes, ui, diagnostics)
		if code != 0 || !ok {
			if code != 0 {
				return "", false, code
			}
			return setupProviderRequired(ui)
		}
		current = selected
		if selected == llm.OpenRouter && diagnostics[selected].Available {
			ui.success("Using OpenRouter.")
			return selected, true, 0
		}
		if selected == llm.OpenRouter && ui.interactive && !yes {
			ready, credential, code := configureSetupOpenRouter(ctx, *rt, cfg, ui)
			if code == setupChooseDifferentProvider {
				if explicit != "" {
					return setupProviderRequired(ui)
				}
				continue
			}
			if code != 0 {
				return "", false, code
			}
			if ready {
				*pendingOpenRouter = credential
				return selected, true, 0
			}
			if explicit != "" || !ui.interactive || yes {
				return setupProviderRequired(ui)
			}
			again, err := ui.askYesNo("Choose a different provider?", true)
			if err != nil {
				return "", false, 130
			}
			if !again {
				return setupProviderRequired(ui)
			}
			continue
		}
		if selected == llm.Claude {
			changed, err := configureSetupClaudeExecutable(cfg, ui)
			if err != nil {
				return "", false, 130
			}
			if changed {
				reconfigured, err := bootstrap.ReconfigureProviders(*rt, *cfg)
				if err != nil {
					fmt.Fprintln(ui.streams.Err, "humansh: selected Claude executable could not be prepared:", err)
					return "", false, protocol.ExitConfig
				}
				*rt = reconfigured
				provider, _ := rt.Engine.Providers.Get(llm.Claude)
				ui.withLoader("Rechecking Claude Code…", func() {
					diagnostics[llm.Claude] = provider.Diagnose(ctx)
				})
				if ctx.Err() != nil {
					return "", false, 130
				}
			}
		}
		if selected == llm.Cursor {
			changed, err := configureSetupCursorExecutable(cfg, ui)
			if err != nil {
				return "", false, 130
			}
			if changed {
				reconfigured, err := bootstrap.ReconfigureProviders(*rt, *cfg)
				if err != nil {
					fmt.Fprintln(ui.streams.Err, "humansh: selected Cursor executable could not be prepared:", err)
					return "", false, protocol.ExitConfig
				}
				*rt = reconfigured
				provider, _ := rt.Engine.Providers.Get(llm.Cursor)
				ui.withLoader("Rechecking Cursor CLI…", func() {
					diagnostics[llm.Cursor] = provider.Diagnose(ctx)
				})
				if ctx.Err() != nil {
					return "", false, 130
				}
			}
		}

		for {
			stopIfUnavailable := explicit != "" && (!ui.interactive || yes)
			ready, diagnostic, code := activateSetupProvider(ctx, *rt, selected, diagnostics[selected], stopIfUnavailable, ui)
			diagnostics[selected] = diagnostic
			if code != 0 || ready {
				return selected, ready, code
			}
			if !ui.interactive || yes {
				return setupProviderRequired(ui)
			}

			ui.note("Fix the issue above, then return here to retry " + setupProviderName(selected) + ".")
			if explicit == "" {
				ui.note("Answer no to return to the provider list, or press Ctrl-C to cancel setup.")
			} else {
				ui.note("Answer no to stop setup, or press Ctrl-C to cancel.")
			}
			retry, err := ui.askYesNo("Retry "+setupProviderName(selected)+"?", true)
			if err != nil {
				return "", false, 130
			}
			if retry {
				continue
			}
			if explicit != "" {
				return setupProviderRequired(ui)
			}
			break
		}
	}
}

func setupProviderRequired(ui *setupUI) (llm.ProviderID, bool, int) {
	ui.warning("Setup stopped: one ready AI provider is required.")
	ui.note("No credential, configuration, or shell file was changed.")
	ui.note("Next: rerun humansh setup and choose ready Codex, Claude, or Cursor, or configure a working OpenRouter key and model.")
	return "", false, protocol.ExitProviderUnavailable
}

func configureSetupOpenRouter(ctx context.Context, rt bootstrap.Runtime, cfg *config.RuntimeConfig, ui *setupUI) (bool, *setupOpenRouterCredential, int) {
	ui.note("OpenRouter is metered: each translation uses API credits, not a Codex or Claude subscription.")
	fmt.Fprintln(ui.streams.Out, "  1. Open https://openrouter.ai/settings/keys, sign in, create a key, and copy it.")
	fmt.Fprintln(ui.streams.Out, "  2. Open https://openrouter.ai/models and copy the model ID shown as provider/model.")
	ui.note("Paste both values below. Humansh validates the key and model capability for free, then runs one small automatic compatibility request before saving anything.")

	if rt.ProviderSetup == nil {
		fmt.Fprintln(ui.streams.Err, "humansh: OpenRouter setup is unavailable in this build.")
		return false, nil, protocol.ExitInternal
	}

	key, err := config.LoadOpenRouterKey(rt.Paths)
	if err != nil {
		ui.warning("The stored OpenRouter key could not be read safely: " + err.Error())
		return false, nil, 0
	}
	credential := &setupOpenRouterCredential{}
	if key == "" {
		for {
			key, err = ui.promptSecret("Paste OpenRouter API key (input hidden)")
			if err != nil {
				return false, nil, 130
			}
			if err := config.ValidateOpenRouterCredential(key); err != nil {
				ui.warning(err.Error() + ". Nothing was saved; try again.")
				continue
			}
			credential.key = key
			break
		}
		ui.note("The key is held only in memory and will be saved after the final setup confirmation.")
	} else if os.Getenv("OPENROUTER_API_KEY") != "" {
		ui.success("Using OPENROUTER_API_KEY from this shell; humansh will not persist it.")
	} else {
		ui.success("Using the stored OpenRouter API key; its value will not be displayed.")
	}

	ui.note("At the model prompt, paste a provider/model ID. Type back to choose another AI provider.")
	keyValidated := false
	for {
		current := cfg.OpenRouter.Model
		promptDefault := current
		if promptDefault == "" {
			promptDefault = "required"
		}
		model, promptErr := ui.prompt("OpenRouter model ID (provider/model)", promptDefault)
		if promptErr != nil {
			return false, nil, 130
		}
		if model == "" {
			model = current
		}
		if strings.EqualFold(model, "back") {
			return false, nil, setupChooseDifferentProvider
		}
		if model == "" {
			ui.warning("Paste a concrete model ID from https://openrouter.ai/models; automatic routing is not safe for strict output.")
			continue
		}
		candidate := *cfg
		candidate.Provider = llm.OpenRouter
		setSetupModel(&candidate, model)
		if err := candidate.Validate(); err != nil {
			ui.warning("That model ID is invalid: " + err.Error())
			continue
		}

		if !keyValidated {
			var keyErr error
			ui.withLoader("Checking the OpenRouter API key without using model credits…", func() {
				keyErr = rt.ProviderSetup.ValidateOpenRouterKey(ctx, candidate, model, key)
			})
			if ctx.Err() != nil {
				return false, nil, 130
			}
			if keyErr != nil {
				ui.warning("OpenRouter key check failed: " + setupProviderErrorTitle(keyErr))
				if os.Getenv("OPENROUTER_API_KEY") != "" {
					ui.note("Next: replace or unset OPENROUTER_API_KEY in this shell, then choose OpenRouter again.")
				} else if credential.key != "" {
					retry, retryErr := ui.askYesNo("Paste a different OpenRouter API key?", true)
					if retryErr != nil {
						return false, nil, 130
					}
					if retry {
						for {
							replacement, promptErr := ui.promptSecret("Paste a different OpenRouter API key (input hidden)")
							if promptErr != nil {
								return false, nil, 130
							}
							if validateErr := config.ValidateOpenRouterCredential(replacement); validateErr != nil {
								ui.warning(validateErr.Error() + ". Nothing was saved; try again.")
								continue
							}
							key = replacement
							credential.key = replacement
							break
						}
						continue
					}
					ui.note("Next: check the key at https://openrouter.ai/settings/keys, then choose OpenRouter again.")
				} else {
					ui.note("Next: check the stored key at https://openrouter.ai/settings/keys, or set OPENROUTER_API_KEY to a valid key and choose OpenRouter again.")
				}
				return false, nil, 0
			}
			keyValidated = true
			ui.success("API key is valid; the key check used no model credits.")
		}
		var modelErr error
		ui.withLoader("Checking whether "+model+" supports strict structured output without using model credits…", func() {
			modelErr = rt.ProviderSetup.ValidateOpenRouterModel(ctx, candidate, model, key)
		})
		if ctx.Err() != nil {
			return false, nil, 130
		}
		if modelErr != nil {
			ui.warning(setupProviderErrorTitle(modelErr))
			if typed, ok := usererr.As(modelErr); ok && typed.Code == "openrouter_structured_output_unsupported" {
				ui.note(typed.Summary)
			}
			ui.note("Compatible models: https://openrouter.ai/models?order=newest&supported_parameters=structured_outputs")
			ui.note("Next: paste another compatible provider/model ID below, or type back.")
			continue
		}
		ui.success("The model advertises strict structured-output support; the capability check used no model credits.")
		var probeErr error
		ui.withLoader("Running the required minimal compatibility check with "+model+" (one small metered request)…", func() {
			_, probeErr = rt.ProviderSetup.ProbeOpenRouter(ctx, candidate, model, key)
		})
		if ctx.Err() != nil {
			return false, nil, 130
		}
		if probeErr != nil {
			ui.warning("That model did not pass the compatibility probe: " + setupProviderErrorTitle(probeErr))
			ui.note("Next: paste another compatible provider/model ID below, or type back.")
			continue
		}
		candidate.OpenRouter.StructuredOutputProven = true
		candidate.OpenRouter.StructuredOutputModel = model
		*cfg = candidate
		ui.success("OpenRouter is ready with " + model + "; settings will be saved after final confirmation.")
		return true, credential, 0
	}
}

func setupProviderErrorTitle(err error) string {
	if typed, ok := usererr.As(err); ok {
		return typed.Title
	}
	return err.Error()
}

func diagnoseSetupProviders(ctx context.Context, rt bootstrap.Runtime) map[llm.ProviderID]llm.Diagnostic {
	diagnostics := make(map[llm.ProviderID]llm.Diagnostic, 4)
	for _, id := range []llm.ProviderID{llm.Codex, llm.Claude, llm.Cursor, llm.OpenRouter} {
		if provider, ok := rt.Engine.Providers.Get(id); ok {
			diagnostics[id] = provider.Diagnose(ctx)
		}
	}
	return diagnostics
}

func chooseSetupProvider(current llm.ProviderID, explicit string, yes bool, ui *setupUI, diagnostics map[llm.ProviderID]llm.Diagnostic) (llm.ProviderID, bool, int) {
	order := []llm.ProviderID{llm.Codex, llm.Claude, llm.Cursor, llm.OpenRouter}
	if explicit != "" {
		id := llm.ProviderID(explicit)
		if _, ok := diagnostics[id]; !ok {
			fmt.Fprintf(ui.streams.Err, "Unknown provider %q. Choose codex, claude, cursor, or openrouter.\n", explicit)
			return "", false, 2
		}
		return id, true, 0
	}

	if !ui.interactive || yes {
		if diagnostic, ok := diagnostics[current]; ok && setupProviderSelectable(current, diagnostic) {
			return current, true, 0
		}
		for _, id := range order {
			if setupProviderSelectable(id, diagnostics[id]) {
				return id, true, 0
			}
		}
		return "", false, 0
	}

	defaultChoice := 1
	for index, id := range order {
		if id == current {
			defaultChoice = index + 1
		}
		currentLabel := ""
		if id == current {
			currentLabel = "  (current)"
		}
		fmt.Fprintf(ui.streams.Out, "    %d  %-13s %s%s\n", index+1, setupProviderName(id), setupProviderChoiceStatus(id, diagnostics[id]), currentLabel)
	}
	for {
		answer, err := ui.prompt("AI provider", strconv.Itoa(defaultChoice))
		if err != nil {
			return "", false, 130
		}
		if answer == "" {
			return order[defaultChoice-1], true, 0
		}
		switch strings.ToLower(answer) {
		case "codex":
			return llm.Codex, true, 0
		case "claude":
			return llm.Claude, true, 0
		case "cursor":
			return llm.Cursor, true, 0
		case "openrouter":
			return llm.OpenRouter, true, 0
		}
		choice, err := strconv.Atoi(answer)
		if err == nil && choice >= 1 && choice <= len(order) {
			return order[choice-1], true, 0
		}
		ui.warning("Choose 1, 2, 3, or 4 (or type codex, claude, cursor, or openrouter).")
	}
}

func setupProviderSelectable(id llm.ProviderID, diagnostic llm.Diagnostic) bool {
	if diagnostic.Available {
		return true
	}
	if id == llm.OpenRouter {
		return false
	}
	return diagnostic.Installed && diagnostic.Configured
}

func activateSetupProvider(ctx context.Context, rt bootstrap.Runtime, id llm.ProviderID, diagnostic llm.Diagnostic, stopIfUnavailable bool, ui *setupUI) (bool, llm.Diagnostic, int) {
	provider, exists := rt.Engine.Providers.Get(id)
	if !exists {
		diagnostic.Message = "provider adapter is unavailable in this build"
	} else {
		ui.note("The live check sends one constant minimal prompt and may consume a small amount of provider quota.")
		ui.withLoader("Checking "+setupProviderName(id)+" with its normal inference command…", func() {
			diagnostic = provider.Probe(ctx)
		})
		if ctx.Err() != nil {
			return false, diagnostic, 130
		}
	}
	if diagnostic.Available {
		ui.success("Using " + setupProviderName(id) + ".")
		return true, diagnostic, 0
	}

	ui.providerProblem(id, diagnostic)
	ui.providerRecovery(id, diagnostic)
	if stopIfUnavailable {
		fmt.Fprintf(ui.streams.Err, "%s is not ready; setup made no changes.\n", setupProviderName(id))
		return false, diagnostic, protocol.ExitProviderUnavailable
	}
	return false, diagnostic, 0
}

func setupProviderChoiceStatus(id llm.ProviderID, diagnostic llm.Diagnostic) string {
	if diagnostic.Available && (diagnostic.LiveCheck || id == llm.OpenRouter) {
		switch id {
		case llm.Codex:
			return "Ready — provider-managed"
		case llm.Claude:
			return "Ready — provider-managed"
		case llm.Cursor:
			return "Ready — provider-managed"
		case llm.OpenRouter:
			return "Ready — metered"
		}
		return "Ready"
	}
	if !diagnostic.Installed {
		return "Not installed"
	}
	if id == llm.OpenRouter {
		if diagnostic.AuthMode == "missing" {
			return "Not configured — metered"
		}
		return "Setup needed — metered"
	}
	if diagnostic.Installed && !diagnostic.LiveCheck {
		return "Installed — live check pending"
	}
	if diagnostic.LiveCheck {
		return "Live check failed"
	}
	return "Needs attention"
}

func readerIsTerminal(reader io.Reader) bool {
	file, ok := reader.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func runDoctor(ctx context.Context, args []string, rt bootstrap.Runtime, streams IO) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(streams.Err)
	providerName := fs.String("provider", "", "diagnose one provider")
	fix := fs.Bool("fix", false, "repair local deterministic setup")
	jsonFlag := fs.Bool("json", false, "JSON output")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return 2
	}
	targetShells := installedShellIDs(rt.Paths, rt.Config.Shell.Name)
	shellDiagnostics := make(map[shell.ID]shell.Diagnostic, len(targetShells))
	registeredShells := make(map[shell.ID]bool, len(targetShells))
	for _, id := range targetShells {
		adapter, registered := rt.Engine.Shells.Get(id)
		registeredShells[id] = registered
		if registered {
			shellDiagnostics[id] = adapter.Diagnose(ctx)
		}
	}
	if *fix {
		for _, id := range targetShells {
			if !registeredShells[id] {
				fmt.Fprintf(streams.Err, "cannot repair %s integration: shell adapter is unavailable\n", shellDisplayName(id))
				return protocol.ExitConfig
			}
		}
		if err := config.RepairPermissions(rt.Paths); err != nil {
			fmt.Fprintln(streams.Err, err)
			return protocol.ExitConfig
		}
		configUsable := true
		for _, issue := range rt.LoadIssues {
			if strings.HasPrefix(issue, "configuration file missing") || strings.HasPrefix(issue, "configuration file is malformed") {
				configUsable = false
			}
		}
		if configUsable {
			if _, err := config.SetupWithOptions(rt.Paths, rt.Config, version.Version, config.SetupOptions{Repair: true, Shells: targetShells}); err != nil {
				fmt.Fprintln(streams.Err, err)
				return protocol.ExitConfig
			}
		}
		refreshed, err := bootstrap.BuildDiagnostic()
		if err != nil {
			fmt.Fprintln(streams.Err, err)
			return protocol.ExitConfig
		}
		rt = refreshed
	}
	issues := append([]string(nil), rt.LoadIssues...)
	issues = append(issues, config.Doctor(rt.Paths, rt.Config, version.Version)...)
	for _, id := range targetShells {
		if !registeredShells[id] {
			issues = append(issues, fmt.Sprintf("%s shell adapter is unavailable", shellDisplayName(id)))
		} else if diagnostic := shellDiagnostics[id]; !diagnostic.Available {
			issues = append(issues, fmt.Sprintf("%s is unavailable: %s", shellDisplayName(id), diagnostic.Message))
		}
	}
	for _, conflict := range config.OverrideConflicts(rt.Overrides) {
		issues = append(issues, conflict)
	}
	providers := map[string]llm.Diagnostic{}
	if *providerName != "" {
		id := llm.ProviderID(*providerName)
		provider, ok := rt.Engine.Providers.Get(id)
		if !ok {
			fmt.Fprintf(streams.Err, "Unknown provider %q. Choose codex, claude, cursor, or openrouter.\n", *providerName)
			return 2
		}
		providers[string(id)] = provider.Diagnose(ctx)
	} else {
		for _, p := range rt.Engine.Providers.List() {
			providers[string(p.ID())] = p.Diagnose(ctx)
		}
	}
	result := map[string]any{"issues": issues, "providers": providers}
	if *jsonFlag {
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Fprintln(streams.Out, string(data))
	} else {
		if len(issues) == 0 {
			fmt.Fprintln(streams.Out, "Local setup: ok")
		} else {
			for _, issue := range issues {
				fmt.Fprintln(streams.Out, "-", issue)
			}
		}
		providerNames := make([]string, 0, len(providers))
		for name := range providers {
			providerNames = append(providerNames, name)
		}
		sort.Strings(providerNames)
		for _, name := range providerNames {
			d := providers[name]
			fmt.Fprintf(streams.Out, "Provider %s: %s auth=%s %s\n", name, setupProviderChoiceStatus(llm.ProviderID(name), d), d.AuthMode, d.Message)
			printProviderRecovery(streams.Out, d, "  ")
		}
	}
	if len(issues) > 0 {
		return protocol.ExitConfig
	}
	return 0
}

func printProviderRecovery(writer io.Writer, diagnostic llm.Diagnostic, indent string) {
	if diagnostic.Executable != "" {
		fmt.Fprintf(writer, "%sExecutable: %q\n", indent, diagnostic.Executable)
	}
	for index, action := range diagnostic.NextSteps {
		label := "Next"
		if index > 0 {
			label = "Then"
		}
		fmt.Fprintf(writer, "%s%s: %s — `%s`\n", indent, label, action.Description, action.Command)
	}
}

func printUnavailableProvider(writer io.Writer, id llm.ProviderID, diagnostic llm.Diagnostic) {
	fmt.Fprintf(writer, "Provider %s is not ready: %s.\n", id, diagnostic.Message)
	if diagnostic.Executable != "" || len(diagnostic.NextSteps) > 0 {
		printProviderRecovery(writer, diagnostic, "  ")
		return
	}
	fmt.Fprintf(writer, "Next: run `humansh provider configure %s`.\n", id)
}

func runVersion(args []string, streams IO) int {
	if len(args) > 1 || len(args) == 1 && args[0] != "--json" {
		return 2
	}
	info := version.Current(protocol.Version, protocol.ReadlineVersion)
	for _, arg := range args {
		if arg == "--json" {
			data, _ := json.MarshalIndent(info, "", "  ")
			fmt.Fprintln(streams.Out, string(data))
			return 0
		}
	}
	fmt.Fprintf(streams.Out, "humansh %s (%s, %s) %s protocols=%s\n", info.Version, info.Commit, info.BuildDate, info.GoVersion, strings.Join(info.Protocols, ","))
	return 0
}

func runUninstall(_ context.Context, purge, yes bool, streams IO) int {
	if yes && !purge {
		fmt.Fprintln(streams.Err, "--yes is valid only with --purge")
		return 2
	}
	confirmedPurge := false
	if purge {
		confirmedPurge = yes
		if !confirmedPurge {
			fmt.Fprint(streams.Out, "Purge humansh configuration and credentials? [y/N] ")
			answer, _ := bufio.NewReader(streams.In).ReadString('\n')
			switch strings.ToLower(strings.TrimSpace(answer)) {
			case "y", "yes":
				confirmedPurge = true
			default:
				fmt.Fprintln(streams.Out, "Uninstall cancelled. Nothing was changed.")
				return 0
			}
		}
	}
	paths, err := config.ResolvePaths()
	if err == nil {
		_, err = config.Uninstall(paths, config.UninstallOptions{Purge: confirmedPurge})
	}
	if err != nil {
		return renderError(streams, usererr.WithExit(protocol.ExitConfig, "uninstall", "humansh uninstall did not complete safely.", "No unrelated paths were targeted.", false, err,
			usererr.Fix{Description: "Repair local setup, then retry with", Command: "humansh doctor --fix"}), false)
	}
	if confirmedPurge {
		fmt.Fprintln(streams.Out, "humansh uninstalled; configuration and credentials were purged.")
	} else {
		fmt.Fprintln(streams.Out, "humansh uninstalled; configuration and credentials were preserved.")
	}
	fmt.Fprintln(streams.Out, "This command cannot alter the parent shell process. If humansh is already loaded there, restart that shell or open a new terminal to unload its in-memory bindings.")
	return 0
}

func renderError(streams IO, err error, debug bool) int {
	if typed, ok := usererr.As(err); ok {
		fmt.Fprintln(streams.Err, usererr.Render(typed, debug))
		if typed.ExitCode != 0 {
			return typed.ExitCode
		}
		return protocol.ExitInternal
	}
	fmt.Fprintln(streams.Err, "humansh: unexpected internal error\nNothing was changed or executed.")
	if debug {
		fmt.Fprintln(streams.Err, "Debug:", usererr.RedactDebug(fmt.Sprint(err)))
	}
	return protocol.ExitInternal
}
func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("input exceeds %d bytes", limit)
	}
	return data, nil
}
func appendUnique(values []string, value string) []string {
	for _, item := range values {
		if item == value {
			return values
		}
	}
	return append(values, value)
}
func remove(values []string, value string) []string {
	out := values[:0]
	for _, item := range values {
		if item != value {
			out = append(out, item)
		}
	}
	return out
}

// configKeys is the supported configuration surface, in display order. It is the
// single source of truth shared by `config list`, `config get`, and the
// unknown-key error, so the three can never drift apart.
var configKeys = []string{
	"provider",
	"ambiguity_policy",
	"timeout_seconds",
	"working_context",
	"shell.smart_enter",
	"shell.clear_line_binding",
	"shell.force_translate_binding",
	"shell.force_literal_binding",
	"providers.codex.model",
	"providers.claude.binary",
	"providers.claude.model",
	"providers.cursor.binary",
	"providers.cursor.model",
	"providers.openrouter.model",
}

func configGet(c config.RuntimeConfig, key string) (string, bool) {
	switch key {
	case "provider":
		return string(c.Provider), true
	case "ambiguity_policy":
		return c.AmbiguityPolicy, true
	case "timeout_seconds":
		return strconv.Itoa(int(c.Timeout.Seconds())), true
	case "working_context":
		return c.WorkingContext, true
	case "shell.smart_enter":
		return strconv.FormatBool(c.Shell.SmartEnter), true
	case "shell.clear_line_binding":
		return c.Shell.ClearLineBinding, true
	case "shell.force_translate_binding":
		return c.Shell.ForceTranslateBinding, true
	case "shell.force_literal_binding":
		return c.Shell.ForceLiteralBinding, true
	case "providers.codex.model":
		return c.Codex.Model, true
	case "providers.claude.binary":
		if c.Claude.Binary == "" {
			return "auto", true
		}
		return c.Claude.Binary, true
	case "providers.claude.model":
		return c.Claude.Model, true
	case "providers.cursor.binary":
		if c.Cursor.Binary == "" {
			return "auto", true
		}
		return c.Cursor.Binary, true
	case "providers.cursor.model":
		return c.Cursor.Model, true
	case "providers.openrouter.model":
		return c.OpenRouter.Model, true
	default:
		return "", false
	}
}
func configSet(c *config.RuntimeConfig, key, value string) error {
	switch key {
	case "provider":
		return fmt.Errorf("use `humansh provider use codex|claude|cursor|openrouter` so availability is verified")
	case "ambiguity_policy":
		c.AmbiguityPolicy = value
	case "timeout_seconds":
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return err
		}
		c.Timeout = time.Duration(parsed) * time.Second
	case "working_context":
		c.WorkingContext = value
	case "shell.smart_enter":
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return err
		}
		c.Shell.SmartEnter = parsed
	case "shell.clear_line_binding":
		c.Shell.ClearLineBinding = value
	case "shell.force_translate_binding":
		c.Shell.ForceTranslateBinding = value
	case "shell.force_literal_binding":
		c.Shell.ForceLiteralBinding = value
	case "providers.codex.model":
		c.Codex.Model = value
	case "providers.claude.binary":
		if value == "auto" {
			value = ""
		}
		c.Claude.Binary = value
	case "providers.claude.model":
		c.Claude.Model = value
	case "providers.cursor.binary":
		if value == "auto" {
			value = ""
		}
		c.Cursor.Binary = value
	case "providers.cursor.model":
		c.Cursor.Model = value
	case "providers.openrouter.model":
		c.OpenRouter.Model = value
		c.OpenRouter.StructuredOutputProven = false
		c.OpenRouter.StructuredOutputModel = ""
	default:
		return fmt.Errorf("unsupported config key %q", key)
	}
	return c.Validate()
}
func usage(out io.Writer) {
	fmt.Fprintln(out, `humansh — natural-language commands for Zsh and Bash

Usage:
  humansh setup
  humansh smart|translate|analyze [protocol flags] < input
  humansh classify [--json] < input
  humansh classifier list|add-command|remove-command|add-english-prefix|remove-english-prefix
  humansh provider <list|use|select|configure|test|help>
  humansh config get|set|list
  humansh onboarding [zsh|bash]
  humansh doctor [--fix] [--json]
  humansh uninstall [--purge] [--yes]
  humansh version [--json]`)
}
