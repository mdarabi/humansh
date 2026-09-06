package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/agenticlab-ai/humansh/internal/bootstrap"
	"github.com/agenticlab-ai/humansh/internal/config"
	"github.com/agenticlab-ai/humansh/internal/shell"
	"github.com/agenticlab-ai/humansh/internal/shell/protocol"
)

const onboardingExample = "list files"

func runOnboarding(_ context.Context, args []string, rt bootstrap.Runtime, streams IO) int {
	if len(args) > 1 {
		fmt.Fprintln(streams.Err, "Usage: humansh onboarding [zsh|bash]")
		return 2
	}

	state, err := config.LoadInstallState(rt.Paths.InstallState)
	if err != nil {
		fmt.Fprintln(streams.Err, "Humansh shell onboarding is available after shell setup is complete.")
		fmt.Fprintln(streams.Err, "Next: run `humansh setup`, then run `humansh onboarding` again.")
		return protocol.ExitConfig
	}
	configured := state.ShellIDs()
	if len(configured) == 0 {
		fmt.Fprintln(streams.Err, "No Humansh shell integration is configured.")
		fmt.Fprintln(streams.Err, "Next: run `humansh setup`, then run `humansh onboarding` again.")
		return protocol.ExitConfig
	}

	var requested shell.ID
	if len(args) == 1 {
		requested = shell.ID(strings.ToLower(args[0]))
		if requested != shell.Zsh && requested != shell.Bash {
			fmt.Fprintf(streams.Err, "Unknown shell %q. Choose zsh or bash.\n", args[0])
			return 2
		}
		if !onboardingHasShell(configured, requested) {
			fmt.Fprintf(streams.Err, "%s onboarding is unavailable because its Humansh integration is not configured.\n", shellDisplayName(requested))
			fmt.Fprintf(streams.Err, "Next: run `humansh setup --shell %s`, then try again.\n", requested)
			return protocol.ExitConfig
		}
	}

	interactive := readerIsTerminal(streams.In) && writerIsTerminal(streams.Out)
	ui := newSetupUI(streams, interactive)
	printOnboardingFlow(configured, requested, rt.Config, ui)
	return 0
}

func printOnboardingFlow(configured []shell.ID, requested shell.ID, cfg config.RuntimeConfig, ui *setupUI) {
	fmt.Fprintln(ui.streams.Out)
	fmt.Fprintln(ui.streams.Out, ui.paint(ansiBold+ansiCyan, "Getting started with Humansh"))

	if requested != "" {
		printShellOnboarding(requested, cfg, ui)
		printOnboardingFooter(ui)
		return
	}

	hasZsh := onboardingHasShell(configured, shell.Zsh)
	hasBash := onboardingHasShell(configured, shell.Bash)
	if hasZsh {
		printShellOnboarding(shell.Zsh, cfg, ui)
	}
	if hasBash && !hasZsh {
		printShellOnboarding(shell.Bash, cfg, ui)
	} else if hasBash {
		fmt.Fprintln(ui.streams.Out, "  Bash is configured too. Run `humansh onboarding bash` for its guide.")
	}
	printOnboardingFooter(ui)
}

func printShellOnboarding(id shell.ID, cfg config.RuntimeConfig, ui *setupUI) {
	name := shellDisplayName(id)
	fmt.Fprintln(ui.streams.Out)
	fmt.Fprintln(ui.streams.Out, ui.paint(ansiBold, name+" quick start"))
	fmt.Fprintf(ui.streams.Out, "  Open a new %s terminal.\n", name)
	fmt.Fprintln(ui.streams.Out, "  Try: "+ui.paint(ansiCyan, onboardingExample))

	translationKey := config.BindingLabel(cfg.Shell.ForceTranslateBinding)
	if id == shell.Zsh && cfg.Shell.SmartEnter {
		fmt.Fprintln(ui.streams.Out, "  Press Enter to translate it. Review the command, then press Enter again to run it.")
	} else if id == shell.Bash {
		fmt.Fprintf(ui.streams.Out, "  Press %s to translate it. Review the command, then press Enter to run it.\n", ui.paint(ansiBold, translationKey))
	} else {
		fmt.Fprintf(ui.streams.Out, "  Press %s to translate it. Review the command, then press Enter to run it.\n", ui.paint(ansiBold, translationKey))
	}
	fmt.Fprintf(ui.streams.Out, "  Edit it or press %s to clear. Nothing runs before review.\n", ui.paint(ansiBold, config.BindingLabel(cfg.Shell.ClearLineBinding)))
}

func printOnboardingFooter(ui *setupUI) {
	fmt.Fprintln(ui.streams.Out)
	fmt.Fprintln(ui.streams.Out, "  Repeat anytime: `humansh onboarding [zsh|bash]`.")
}

func onboardingHasShell(configured []shell.ID, target shell.ID) bool {
	for _, id := range configured {
		if id == target {
			return true
		}
	}
	return false
}
