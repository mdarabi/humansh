package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/agenticlab-ai/humansh/internal/config"
	"github.com/agenticlab-ai/humansh/internal/shell"
	"github.com/agenticlab-ai/humansh/internal/shell/protocol"
)

func TestOnboardingBeforeSetupShowsTheNextStep(t *testing.T) {
	isolatedEnv(t)
	var out, errOut bytes.Buffer
	code := Run(context.Background(), []string{"onboarding"}, IO{In: strings.NewReader(""), Out: &out, Err: &errOut})
	if code != protocol.ExitConfig || !strings.Contains(errOut.String(), "available after shell setup is complete") || !strings.Contains(errOut.String(), "humansh setup") {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
}

func TestZshOnboardingTeachesTwoStepReviewWithConfiguredBindings(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Shell.ForceTranslateBinding = "^R"
	cfg.Shell.ClearLineBinding = "^U"
	var out bytes.Buffer
	ui := newSetupUI(IO{In: strings.NewReader(""), Out: &out}, false)

	printOnboardingFlow([]shell.ID{shell.Zsh}, "", cfg, ui)

	for _, want := range []string{
		"Getting started with Humansh",
		"Zsh quick start",
		onboardingExample,
		"Press Enter to translate it",
		"press Enter again to run it",
		"press Ctrl-U to clear",
		"Nothing runs before review",
		"humansh onboarding [zsh|bash]",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("Zsh onboarding missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "Bash quick start") {
		t.Fatalf("Zsh-only onboarding included Bash:\n%s", out.String())
	}
}

func TestZshOnboardingUsesForceTranslationWhenSmartEnterIsOff(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Shell.SmartEnter = false
	cfg.Shell.ForceTranslateBinding = "^X^T"
	var out bytes.Buffer
	ui := newSetupUI(IO{In: strings.NewReader(""), Out: &out}, false)

	printOnboardingFlow([]shell.ID{shell.Zsh}, shell.Zsh, cfg, ui)

	if !strings.Contains(out.String(), "Press Ctrl-X then Ctrl-T to translate it") || strings.Contains(out.String(), "Press Enter to translate it") {
		t.Fatalf("Zsh onboarding ignored configured Smart Enter mode:\n%s", out.String())
	}
}

func TestOnboardingMentionsBashWithoutAddingAnotherPrompt(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Shell.ForceTranslateBinding = "^R"
	var out bytes.Buffer
	ui := newSetupUI(IO{In: strings.NewReader(""), Out: &out}, true)
	printOnboardingFlow([]shell.ID{shell.Zsh, shell.Bash}, "", cfg, ui)

	if !strings.Contains(out.String(), "Bash is configured too. Run `humansh onboarding bash` for its guide.") {
		t.Fatalf("Bash guide command was not shown:\n%s", out.String())
	}
	for _, unwanted := range []string{"Show the Bash walkthrough too?", "Bash quick start"} {
		if strings.Contains(out.String(), unwanted) {
			t.Fatalf("onboarding included %q:\n%s", unwanted, out.String())
		}
	}
}

func TestRequestedBashOnboardingUsesTheTranslateShortcut(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Shell.ForceTranslateBinding = "^R"
	var out bytes.Buffer
	ui := newSetupUI(IO{In: strings.NewReader(""), Out: &out}, false)

	printOnboardingFlow([]shell.ID{shell.Zsh, shell.Bash}, shell.Bash, cfg, ui)

	for _, want := range []string{"Bash quick start", "Press Ctrl-R to translate it", "press Enter to run it", "Nothing runs before review"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("Bash onboarding missing %q:\n%s", want, out.String())
		}
	}
}
