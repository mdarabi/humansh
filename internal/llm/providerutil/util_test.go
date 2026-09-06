package providerutil

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	usererr "github.com/agenticlab-ai/humansh/internal/errors"
	"github.com/agenticlab-ai/humansh/internal/llm"
	"github.com/agenticlab-ai/humansh/internal/processrunner"
)

func TestProbeRequiresAnExactResponseMarker(t *testing.T) {
	t.Parallel()
	base := llm.Diagnostic{Installed: true, Configured: true, AuthMode: "provider_managed"}
	success := ProbeDiagnostic(base, llm.Claude, 20*time.Second, processrunner.Result{Stdout: []byte("  " + ProbeMarker + "\n")}, nil)
	if !success.Available || !success.LiveCheck {
		t.Fatalf("exact marker diagnostic=%+v", success)
	}

	echoedPrompt := ProbeDiagnostic(base, llm.Claude, 20*time.Second, processrunner.Result{Stdout: []byte(ProbePrompt)}, nil)
	if echoedPrompt.Available || !echoedPrompt.LiveCheck {
		t.Fatalf("echoed prompt diagnostic=%+v", echoedPrompt)
	}
}

func TestCLIErrorCatalogMappings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, text, code string
		exit             int
	}{
		{"rate-limit", "rate limit reached", "provider_quota", 23},
		{"billing", "billing limit reached", "provider_quota", 23},
		{"auth", "unauthorized login", "provider_auth", 22},
		{"model", "invalid model", "provider_model", 21},
		{"organization", "organization disallowed", "provider_access_denied", 21},
		{"obsolete-cli", "unknown option --json-schema", "provider_too_old", 21},
		{"overloaded", "service overloaded", "provider_temporary", 24},
		{"network", "network connection failed", "provider_temporary", 24},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			typed, ok := usererr.As(MapCLIError(llm.Claude, 20*time.Second, []byte(test.text), errors.New("provider exited")))
			if !ok || typed.Code != test.code || typed.ExitCode != test.exit || len(typed.Fixes) == 0 {
				t.Fatalf("mapping=%+v ok=%t", typed, ok)
			}
		})
	}
}

func TestProbeFailureReturnsProviderMessageWithoutInterpretingIt(t *testing.T) {
	t.Parallel()
	const message = "Error: Authentication required. Please run 'agent login' first."
	diagnostic := ProbeDiagnostic(
		llm.Diagnostic{Installed: true, Configured: true, AuthMode: "provider_managed"},
		llm.Cursor,
		20*time.Second,
		processrunner.Result{Stderr: []byte(message + "\n")},
		errors.New("exit status 1"),
	)
	if diagnostic.Available || !diagnostic.LiveCheck || diagnostic.Message != message {
		t.Fatalf("diagnostic=%+v", diagnostic)
	}
	if len(diagnostic.NextSteps) != 0 {
		t.Fatalf("provider message was interpreted into recovery actions: %+v", diagnostic.NextSteps)
	}
}

func TestTimeoutGuidanceIsSharedByEveryProvider(t *testing.T) {
	t.Parallel()
	for _, provider := range []llm.ProviderID{llm.Codex, llm.Claude, llm.Cursor, llm.OpenRouter} {
		t.Run(string(provider), func(t *testing.T) {
			t.Parallel()
			typed, ok := usererr.As(TemporaryOrTimeout(provider, 20*time.Second, context.DeadlineExceeded))
			if !ok || typed.Code != "provider_timeout" || typed.ExitCode != 24 {
				t.Fatalf("error=%#v", typed)
			}
			rendered := usererr.Render(typed, false)
			for _, want := range []string{
				provider.Label() + " timed out before completing the translation.",
				"configured provider timeout is 20 seconds",
				"humansh config set timeout_seconds 45",
				"humansh provider test " + string(provider),
			} {
				if !strings.Contains(rendered, want) {
					t.Errorf("timeout guidance omitted %q:\n%s", want, rendered)
				}
			}
		})
	}
}

func TestTimeoutAtMaximumDoesNotSuggestInvalidValue(t *testing.T) {
	t.Parallel()
	typed, ok := usererr.As(Timeout(llm.Cursor, 60*time.Second, context.DeadlineExceeded))
	if !ok {
		t.Fatalf("error=%#v", typed)
	}
	rendered := usererr.Render(typed, false)
	if !strings.Contains(rendered, "already at the 60-second maximum") || strings.Contains(rendered, "timeout_seconds 120") {
		t.Fatalf("maximum timeout guidance is wrong:\n%s", rendered)
	}
}

func TestDecodeResponseRequiresExactSchemaShape(t *testing.T) {
	t.Parallel()
	valid := `{"status":"ok","command":"pwd","explanation":"Prints the directory.","clarification":"","assumptions":[]}`
	if _, err := DecodeResponse([]byte(valid)); err != nil {
		t.Fatal(err)
	}
	for name, input := range map[string]string{
		"missing":   `{"status":"ok","command":"pwd","explanation":"","clarification":""}`,
		"null":      `{"status":"ok","command":"pwd","explanation":"","clarification":"","assumptions":null}`,
		"unknown":   `{"status":"ok","command":"pwd","explanation":"","clarification":"","assumptions":[],"risk":"low"}`,
		"multiple":  valid + valid,
		"duplicate": `{"status":"ok","status":"unsupported","command":"pwd","explanation":"","clarification":"","assumptions":[]}`,
	} {
		_, err := DecodeResponse([]byte(input))
		if typed, ok := usererr.As(err); !ok || typed.ExitCode != 25 {
			t.Errorf("%s error=%#v", name, err)
		}
	}
}
