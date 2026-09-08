package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/agenticlab-ai/humansh/internal/bootstrap"
	"github.com/agenticlab-ai/humansh/internal/config"
	"github.com/agenticlab-ai/humansh/internal/llm"
	"github.com/agenticlab-ai/humansh/internal/shell"
	"github.com/agenticlab-ai/humansh/internal/shell/protocol"
	"github.com/agenticlab-ai/humansh/internal/version"
)

// runQuickSetup is the default setup experience. It deliberately keeps
// customization out of the happy path: select an available CLI provider or
// explicitly configure OpenRouter, show the shell file that will change, and
// ask for one final confirmation. The full preference editor lives behind
// --advanced.
func runQuickSetup(ctx context.Context, options setupOptions, rt bootstrap.Runtime, streams IO) int {
	interactive := readerIsTerminal(streams.In) && !options.yes
	ui := newSetupUI(streams, interactive)
	ui.ctx = ctx
	quickSetupHeader(ui)

	hasConfig, err := setupPathExists(rt.Paths.ConfigFile)
	if err != nil {
		fmt.Fprintf(streams.Err, "humansh: cannot inspect the existing configuration: %v\nNothing was changed or executed.\n", err)
		return protocol.ExitConfig
	}
	state, hasState, err := loadQuickInstallState(rt.Paths.InstallState)
	if err != nil && !options.repair {
		fmt.Fprintf(streams.Err, "humansh: existing install state is invalid: %v\nNothing was changed or executed.\nNext: run `humansh setup --repair`.\n", err)
		return protocol.ExitConfig
	}
	cfg := rt.Config
	targetShells, verifyShells, allowShellFallback, err := quickSetupShells(options, cfg, state, hasState)
	if err != nil {
		fmt.Fprintln(streams.Err, err)
		return protocol.ExitConfig
	}
	if err := prepareQuickSetupShells(ctx, &cfg, targetShells, verifyShells, allowShellFallback, hasState && options.shellName == "", options.repair, rt); err != nil {
		if ctx.Err() != nil {
			printSetupCancellation(streams.Out, false)
			return 130
		}
		fmt.Fprintln(streams.Err, err)
		return protocol.ExitConfig
	}

	probeProvider := !hasConfig || options.providerName != ""
	var pendingOpenRouter *setupOpenRouterCredential
	providerProbeComplete := false
	if probeProvider {
		for {
			selected, code := chooseQuickSetupProvider(ctx, cfg.Provider, options.providerName, options.yes, rt, ui)
			if code != 0 {
				if code == 130 {
					printSetupCancellation(streams.Out, pendingOpenRouter != nil)
				}
				return code
			}
			if selected != llm.OpenRouter {
				cfg.Provider = selected
				break
			}

			probeComplete, credential, configureCode := prepareQuickSetupOpenRouter(ctx, rt, &cfg, ui)
			if configureCode == setupChooseDifferentProvider {
				if options.providerName != "" {
					_, _, requiredCode := setupProviderRequired(ui)
					return requiredCode
				}
				continue
			}
			if configureCode != 0 {
				if configureCode == 130 {
					printSetupCancellation(streams.Out, credential != nil)
				}
				return configureCode
			}
			pendingOpenRouter = credential
			providerProbeComplete = probeComplete
			break
		}
	} else if _, ok := rt.Engine.Providers.Get(cfg.Provider); !ok {
		fmt.Fprintf(streams.Err, "humansh: configured provider %q is unavailable in this build.\nNothing was changed or executed.\nNext: run `humansh setup --advanced` to choose another provider.\n", cfg.Provider)
		return protocol.ExitConfig
	}

	reviewedRemovals, err := config.PreviewRemovedStartupChanges(rt.Paths, targetShells, options.repair)
	if err != nil {
		fmt.Fprintf(streams.Err, "humansh: cannot prepare shell-integration changes: %v\nNothing was changed or executed.\n", err)
		return protocol.ExitConfig
	}
	if options.noShellChange && len(reviewedRemovals) > 0 {
		fmt.Fprintln(streams.Err, "humansh: --no-shell-change cannot switch shell integrations because an existing startup block would remain active.\nNothing was changed or executed.")
		return protocol.ExitConfig
	}

	var reviewedStartups []config.StartupChange
	if !options.noShellChange {
		reviewedStartups, err = config.PreviewStartupChanges(rt.Paths, cfg, targetShells, options.repair)
		if err != nil {
			fmt.Fprintf(streams.Err, "humansh: cannot prepare shell startup changes: %v\nNothing was changed or executed.\n", err)
			if config.IsStartupAccessError(err) {
				fmt.Fprintln(streams.Err, "Next: rerun `humansh setup --no-shell-change` to finish setup and print the exact block to add manually.")
			} else if !options.repair {
				fmt.Fprintln(streams.Err, "Next: run `humansh setup --repair` if the Humansh block is damaged or duplicated.")
			}
			return protocol.ExitConfig
		}
	}

	defaultRerun := hasConfig && hasState && options.providerName == "" && options.shellName == "" && !options.repair
	startupChanged := quickStartupChanged(reviewedStartups) || len(reviewedRemovals) > 0
	if defaultRerun && !startupChanged {
		if err := applyQuickSetup(rt, cfg, targetShells, reviewedStartups, reviewedRemovals, options, nil, streams.Err); err != nil {
			printQuickSetupApplyError(err, streams)
			return protocol.ExitConfig
		}
		printQuickSetupResult(ui, "Existing setup kept", cfg, targetShells)
		return 0
	}

	var installedShells []shell.ID
	if hasState {
		installedShells = state.ShellIDs()
	}
	printQuickSetupPlan(cfg, targetShells, reviewedStartups, reviewedRemovals, options.noShellChange, installedShells, pendingOpenRouter, ui)
	if ui.interactive {
		apply, promptErr := ui.askYesNo("Continue?", true)
		if promptErr != nil || !apply {
			printSetupCancellation(streams.Out, pendingOpenRouter != nil)
			return 130
		}
	}
	if ctx.Err() != nil {
		printSetupCancellation(streams.Out, pendingOpenRouter != nil)
		return 130
	}

	if probeProvider && !providerProbeComplete {
		if code := probeQuickSetupProvider(ctx, rt, cfg.Provider, ui); code != 0 {
			if code == 130 {
				printSetupCancellation(streams.Out, false)
			}
			return code
		}
	}

	if err := applyQuickSetup(rt, cfg, targetShells, reviewedStartups, reviewedRemovals, options, pendingOpenRouter, streams.Err); err != nil {
		printQuickSetupApplyError(err, streams)
		return protocol.ExitConfig
	}
	if pendingOpenRouter != nil && pendingOpenRouter.storage != "" {
		ui.success("OpenRouter API key saved to " + pendingOpenRouter.storage + ".")
	}

	if options.noShellChange {
		ui.success("Humansh is ready; shell startup files were not changed.")
		for _, id := range targetShells {
			fmt.Fprintf(streams.Out, "\nAdd this exact block to ~/%s:\n", shellStartupName(id))
			fmt.Fprint(streams.Out, config.ManagedBlockForShell(rt.Paths, cfg, id))
		}
		return 0
	}

	printQuickSetupCelebration(cfg, targetShells, ui)
	return 0
}

func quickSetupHeader(ui *setupUI) {
	fmt.Fprintln(ui.streams.Out)
	fmt.Fprintln(ui.streams.Out, ui.paint(ansiBold+ansiCyan, "humansh setup"))
}

func quickSetupSection(ui *setupUI, title string) {
	fmt.Fprintln(ui.streams.Out)
	fmt.Fprintln(ui.streams.Out, ui.paint(ansiBold+ansiCyan, title))
}

func quickSetupWarningSection(ui *setupUI, title string) {
	fmt.Fprintln(ui.streams.Out)
	fmt.Fprintln(ui.streams.Out, ui.paint(ansiBold+ansiYellow, title))
}

func quickSetupRow(ui *setupUI, label, value string) {
	fmt.Fprintf(ui.streams.Out, "  %-10s %s\n", label, value)
}

func printQuickSetupResult(ui *setupUI, status string, cfg config.RuntimeConfig, shells []shell.ID) {
	ui.success(status)
	fmt.Fprintf(ui.streams.Out, "    %-10s %s\n", "Shells", shellNames(shells))
	fmt.Fprintf(ui.streams.Out, "    %-10s %s\n", "Provider", setupProviderName(cfg.Provider))
	fmt.Fprintf(ui.streams.Out, "    %-10s %s\n", "Settings", "Change with `humansh setup --advanced`")
}

func setupPathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func loadQuickInstallState(path string) (config.InstallState, bool, error) {
	exists, err := setupPathExists(path)
	if err != nil || !exists {
		return config.InstallState{}, false, err
	}
	state, err := config.LoadInstallState(path)
	if err != nil {
		return config.InstallState{}, false, err
	}
	return state, len(state.ShellIDs()) > 0, nil
}

func quickSetupShells(options setupOptions, cfg config.RuntimeConfig, state config.InstallState, hasState bool) ([]shell.ID, bool, bool, error) {
	if options.shellName != "" {
		id := shell.ID(strings.ToLower(options.shellName))
		if id != shell.Zsh && id != shell.Bash {
			return nil, false, false, fmt.Errorf("unsupported shell %q; choose bash or zsh", options.shellName)
		}
		return []shell.ID{id}, true, false, nil
	}
	if hasState {
		return normalizeQuickShells(state.ShellIDs()), false, false, nil
	}
	if options.repair {
		return []shell.ID{cfg.Shell.Name}, false, false, nil
	}
	if login := shell.ID(filepath.Base(os.Getenv("SHELL"))); login == shell.Zsh || login == shell.Bash {
		return []shell.ID{login}, true, false, nil
	}
	if cfg.Shell.Name == shell.Zsh || cfg.Shell.Name == shell.Bash {
		return []shell.ID{cfg.Shell.Name}, true, true, nil
	}
	return []shell.ID{shell.Zsh}, true, true, nil
}

func normalizeQuickShells(ids []shell.ID) []shell.ID {
	normalized := make([]shell.ID, 0, len(ids))
	for _, id := range []shell.ID{shell.Zsh, shell.Bash} {
		if containsShell(ids, id) {
			normalized = append(normalized, id)
		}
	}
	return normalized
}

func prepareQuickSetupShells(ctx context.Context, cfg *config.RuntimeConfig, targetShells []shell.ID, verify, allowFallback, preservePrimary, repair bool, rt bootstrap.Runtime) error {
	if len(targetShells) == 0 {
		return fmt.Errorf("humansh: no supported shell integration is available.\nNothing was changed or executed")
	}
	if verify && allowFallback && len(targetShells) == 1 {
		preferred := targetShells[0]
		for _, id := range []shell.ID{preferred, otherQuickShell(preferred)} {
			adapter, ok := rt.Engine.Shells.Get(id)
			if !ok {
				continue
			}
			diagnostic := adapter.Diagnose(ctx)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if diagnostic.Available {
				targetShells[0] = id
				verify = false
				break
			}
		}
		if verify {
			return fmt.Errorf("humansh: no supported interactive shell is available.\nNothing was changed or executed.\nFix: install Zsh or Bash 4.3+, then rerun `humansh setup`")
		}
	}
	for _, id := range targetShells {
		adapter, ok := rt.Engine.Shells.Get(id)
		if !ok {
			return fmt.Errorf("humansh: %s integration is unavailable in this build.\nNothing was changed or executed", shellDisplayName(id))
		}
		if !verify || repair {
			continue
		}
		diagnostic := adapter.Diagnose(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !diagnostic.Available {
			message := diagnostic.Message
			if message == "" {
				message = setupShellRequirement(id)
			}
			return fmt.Errorf("humansh: %s is unavailable: %s.\nNothing was changed or executed.\nFix: install %s, ensure `%s --version` works, then rerun `humansh setup`", shellDisplayName(id), message, shellDisplayName(id), id)
		}
	}

	if preservePrimary && containsShell(targetShells, cfg.Shell.Name) {
		return nil
	}
	previous := cfg.Shell.Name
	cfg.Shell.Name = targetShells[0]
	if cfg.Shell.Name == shell.Bash {
		cfg.Shell.Protocol = protocol.ReadlineVersion
		cfg.Shell.SmartEnter = false
	} else {
		cfg.Shell.Protocol = protocol.Version
		if previous == shell.Bash {
			cfg.Shell.SmartEnter = true
		}
	}
	return nil
}

func otherQuickShell(id shell.ID) shell.ID {
	if id == shell.Bash {
		return shell.Zsh
	}
	return shell.Bash
}

func chooseQuickSetupProvider(ctx context.Context, current llm.ProviderID, explicit string, yes bool, rt bootstrap.Runtime, ui *setupUI) (llm.ProviderID, int) {
	order := []llm.ProviderID{llm.Codex, llm.Claude, llm.Cursor, llm.OpenRouter}
	if explicit != "" {
		id := llm.ProviderID(strings.ToLower(explicit))
		if id != llm.Codex && id != llm.Claude && id != llm.Cursor && id != llm.OpenRouter {
			fmt.Fprintf(ui.streams.Err, "Unknown provider %q. Choose codex, claude, cursor, or openrouter.\n", explicit)
			return "", 2
		}
		provider, ok := rt.Engine.Providers.Get(id)
		if !ok {
			fmt.Fprintf(ui.streams.Err, "%s is unavailable in this build.\n", setupProviderName(id))
			return "", protocol.ExitProviderUnavailable
		}
		if id == llm.OpenRouter {
			return id, 0
		}
		diagnostic := provider.Diagnose(ctx)
		if ctx.Err() != nil {
			return "", 130
		}
		if !setupProviderSelectable(id, diagnostic) {
			ui.providerProblem(id, diagnostic)
			ui.providerRecovery(id, diagnostic)
			_, _, code := setupProviderRequired(ui)
			return "", code
		}
		return id, 0
	}

	choices := make([]llm.ProviderID, 0, len(order))
	for _, id := range order {
		provider, ok := rt.Engine.Providers.Get(id)
		if !ok {
			continue
		}
		if id == llm.OpenRouter {
			choices = append(choices, id)
			continue
		}
		diagnostic := provider.Diagnose(ctx)
		if setupProviderSelectable(id, diagnostic) {
			choices = append(choices, id)
		}
	}
	if ctx.Err() != nil {
		return "", 130
	}
	if len(choices) == 0 {
		_, _, code := setupProviderRequired(ui)
		return "", code
	}
	if len(choices) == 1 {
		return choices[0], 0
	}

	defaultChoice := 0
	for index, id := range choices {
		if id == current {
			defaultChoice = index
		}
	}
	if !ui.interactive || yes {
		return choices[defaultChoice], 0
	}

	quickSetupSection(ui, "Choose your AI provider")
	for index, id := range choices {
		defaultLabel := ""
		if index == defaultChoice {
			defaultLabel = ui.paint(ansiDim, "default")
		}
		fmt.Fprintf(ui.streams.Out, "  %d  %-12s%s\n", index+1, setupProviderName(id), defaultLabel)
	}
	fmt.Fprintln(ui.streams.Out)
	for {
		answer, err := ui.prompt("AI provider", strconv.Itoa(defaultChoice+1))
		if err != nil {
			return "", 130
		}
		if answer == "" {
			return choices[defaultChoice], 0
		}
		for index, id := range choices {
			if answer == strconv.Itoa(index+1) || strings.EqualFold(answer, string(id)) || strings.EqualFold(answer, setupProviderName(id)) {
				return id, 0
			}
		}
		numbers := make([]string, 0, len(choices))
		for index := range choices {
			numbers = append(numbers, strconv.Itoa(index+1))
		}
		ui.warning("Choose " + strings.Join(numbers, ", ") + ", or type a provider name.")
	}
}

// prepareQuickSetupOpenRouter reuses the complete OpenRouter configuration
// flow from advanced setup. A previously proven model and available key can go
// through the normal quick provider probe; a new configuration is already
// proven by configureSetupOpenRouter and must not incur a second metered call.
func prepareQuickSetupOpenRouter(ctx context.Context, rt bootstrap.Runtime, cfg *config.RuntimeConfig, ui *setupUI) (bool, *setupOpenRouterCredential, int) {
	modelProven := cfg.OpenRouter.Model != "" && cfg.OpenRouter.StructuredOutputProven && cfg.OpenRouter.StructuredOutputModel == cfg.OpenRouter.Model
	if modelProven {
		key, keyErr := config.LoadOpenRouterKey(rt.Paths)
		if keyErr == nil && key != "" {
			cfg.Provider = llm.OpenRouter
			return false, nil, 0
		}
	}
	if !ui.interactive {
		key, keyErr := config.LoadOpenRouterKey(rt.Paths)
		ui.warning("OpenRouter setup needs an API key and model choice.")
		if keyErr != nil {
			ui.note("The existing OpenRouter credential could not be loaded safely.")
		} else if key == "" {
			ui.note("Set OPENROUTER_API_KEY in the environment, or rerun setup interactively to paste a key securely.")
		}
		ui.note("Next: run `humansh setup --provider openrouter` from a terminal.")
		_, _, requiredCode := setupProviderRequired(ui)
		return false, nil, requiredCode
	}

	ready, credential, code := configureSetupOpenRouter(ctx, rt, cfg, ui)
	if code != 0 {
		return false, credential, code
	}
	if !ready {
		_, _, requiredCode := setupProviderRequired(ui)
		return false, credential, requiredCode
	}
	return true, credential, 0
}

func quickStartupChanged(changes []config.StartupChange) bool {
	for _, change := range changes {
		if change.Changed() {
			return true
		}
	}
	return false
}

func printQuickSetupPlan(cfg config.RuntimeConfig, targetShells []shell.ID, startups, removals []config.StartupChange, noShellChange bool, installedShells []shell.ID, pendingOpenRouter *setupOpenRouterCredential, ui *setupUI) {
	quickSetupSection(ui, "Review")
	label := "Shell"
	if len(targetShells) > 1 {
		label = "Shells"
	}
	quickSetupRow(ui, label, shellNames(targetShells))
	quickSetupRow(ui, "Provider", setupProviderName(cfg.Provider))
	if cfg.Provider == llm.OpenRouter {
		quickSetupRow(ui, "Model", cfg.OpenRouter.Model)
		keySource := "Stored key"
		switch {
		case pendingOpenRouter != nil && pendingOpenRouter.key != "":
			keySource = "New key — save securely after confirmation"
		case os.Getenv("OPENROUTER_API_KEY") != "":
			keySource = "OPENROUTER_API_KEY from shell"
		}
		quickSetupRow(ui, "API key", keySource)
	}

	startupSummaries := make([]string, 0, len(startups)+len(removals))
	if noShellChange {
		startupSummaries = append(startupSummaries, "No startup-file changes")
	} else {
		for _, removal := range removals {
			startupSummaries = append(startupSummaries, "Remove Humansh from "+setupDisplayPath(removal.Path))
		}
		for index, startup := range startups {
			if !startup.Changed() {
				continue
			}
			action := "Update"
			if containsShell(installedShells, targetShells[index]) {
				action = "Refresh"
			}
			startupSummaries = append(startupSummaries, action+" "+setupDisplayPath(startup.Path))
		}
		if len(startupSummaries) == 0 {
			startupSummaries = append(startupSummaries, "No startup-file changes")
		}
	}
	for index, summary := range startupSummaries {
		startupLabel := ""
		if index == 0 {
			startupLabel = "Startup"
		}
		quickSetupRow(ui, startupLabel, summary)
	}
	quickSetupRow(ui, "Safety", "Commands wait for your review")
	fmt.Fprintln(ui.streams.Out)
}

func probeQuickSetupProvider(ctx context.Context, rt bootstrap.Runtime, id llm.ProviderID, ui *setupUI) int {
	provider, ok := rt.Engine.Providers.Get(id)
	if !ok {
		fmt.Fprintf(ui.streams.Err, "%s is unavailable in this build; setup made no changes.\n", setupProviderName(id))
		return protocol.ExitProviderUnavailable
	}
	var diagnostic llm.Diagnostic
	ui.withLoader("Checking "+setupProviderName(id)+"…", func() {
		diagnostic = provider.Probe(ctx)
	})
	if ctx.Err() != nil {
		return 130
	}
	if !diagnostic.Available {
		printQuickSetupProviderFailure(id, diagnostic, ui)
		return protocol.ExitProviderUnavailable
	}
	ui.success(setupProviderName(id) + " ready")
	return 0
}

func printQuickSetupProviderFailure(id llm.ProviderID, diagnostic llm.Diagnostic, ui *setupUI) {
	quickSetupWarningSection(ui, setupProviderName(id)+" check failed")
	if diagnostic.Message != "" {
		fmt.Fprintln(ui.streams.Out)
		fmt.Fprintln(ui.streams.Out, "  "+diagnostic.Message)
	}
	fmt.Fprintln(ui.streams.Out)
	fmt.Fprintln(ui.streams.Out, "  Fix the provider issue above, then try again.")
	fmt.Fprintln(ui.streams.Out, "  No Humansh settings were changed.")
}

func applyQuickSetup(rt bootstrap.Runtime, cfg config.RuntimeConfig, targetShells []shell.ID, startups, removals []config.StartupChange, options setupOptions, pendingOpenRouter *setupOpenRouterCredential, errOut io.Writer) error {
	apply := func() error {
		return applySetupWithOpenRouterCredential(rt.Paths, pendingOpenRouter, errOut, func() error {
			_, err := config.SetupWithOptions(rt.Paths, cfg, version.Version, config.SetupOptions{
				NoShellChange:    options.noShellChange,
				Repair:           options.repair,
				Shells:           targetShells,
				ReviewedStartups: startups,
				ReviewedRemovals: removals,
			})
			return err
		})
	}
	if options.repair {
		return apply()
	}
	return rt.Store.SaveAndApply(cfg, apply)
}

func printQuickSetupApplyError(err error, streams IO) {
	fmt.Fprintln(streams.Err, err)
	if config.IsStartupAccessError(err) {
		fmt.Fprintln(streams.Err, "Next: rerun `humansh setup --no-shell-change` to finish setup and print the exact block to add manually.")
	}
}

func printQuickSetupCelebration(cfg config.RuntimeConfig, targetShells []shell.ID, ui *setupUI) {
	fmt.Fprintln(ui.streams.Out)
	fmt.Fprintln(ui.streams.Out, ui.paint(ansiBold+ansiGreen, "🎉 Humansh is ready!"))
	fmt.Fprintln(ui.streams.Out)
	quickSetupRow(ui, "Settings", ui.paint(ansiBold, "`humansh setup --advanced`"))
	trigger := config.BindingLabel(cfg.Shell.ForceTranslateBinding)
	if containsShell(targetShells, shell.Zsh) && cfg.Shell.SmartEnter {
		trigger = "Enter"
	}
	fmt.Fprintln(ui.streams.Out)
	fmt.Fprintf(ui.streams.Out, "  Try it: open a new terminal, type %s, press %s to translate, then Enter to run.\n", ui.paint(ansiBold, "`list files`"), trigger)
}
