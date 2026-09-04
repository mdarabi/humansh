package llm

import (
	"context"
	"fmt"
)

type ProviderID string

const (
	Codex      ProviderID = "codex"
	Claude     ProviderID = "claude"
	Cursor     ProviderID = "cursor"
	OpenRouter ProviderID = "openrouter"
)

// Label is the human-readable provider name shown in shell status messages such
// as "Translating with Codex…". It is a fixed enum-derived string and never
// contains user input, so a shell adapter may render it directly.
func (p ProviderID) Label() string {
	switch p {
	case Codex:
		return "Codex"
	case Claude:
		return "Claude Code"
	case Cursor:
		return "Cursor CLI"
	case OpenRouter:
		return "OpenRouter"
	default:
		return "provider"
	}
}

type Diagnostic struct {
	Installed     bool `json:"installed"`
	Configured    bool `json:"configured"`
	Authenticated bool `json:"authenticated"`
	Available     bool `json:"available"`
	// LiveCheck is true only when Humansh has sent a minimal prompt through the
	// provider's normal inference command. Discovery-only diagnostics leave it
	// false so list/doctor can remain non-billable and side-effect free.
	LiveCheck    bool               `json:"live_check"`
	AuthMode     string             `json:"auth_mode"`
	Executable   string             `json:"executable,omitempty"`
	Version      string             `json:"version,omitempty"`
	Capabilities []string           `json:"capabilities,omitempty"`
	Message      string             `json:"message,omitempty"`
	NextSteps    []DiagnosticAction `json:"next_steps,omitempty"`
}

// DiagnosticAction is a copyable recovery step for an unavailable provider.
// Description explains why the command is useful; Command is intentionally
// separate so terminal UIs and JSON consumers do not need to parse prose.
type DiagnosticAction struct {
	Description string `json:"description"`
	Command     string `json:"command"`
}

type TranslationRequest struct {
	Input          string `json:"input"`
	Shell          string `json:"shell"`
	OS             string `json:"os"`
	Architecture   string `json:"architecture"`
	WorkingContext string `json:"working_context,omitempty"`
}

type TranslationResponse struct {
	Status        string   `json:"status"`
	Command       string   `json:"command"`
	Explanation   string   `json:"explanation"`
	Clarification string   `json:"clarification"`
	Assumptions   []string `json:"assumptions"`
}

type Provider interface {
	ID() ProviderID
	// Diagnose performs non-inference discovery. CLI adapters must not assume
	// that optional login, status, version, or help commands exist.
	Diagnose(ctx context.Context) Diagnostic
	// Probe sends one constant, minimal inference prompt and reports whether the
	// provider's normal route can currently reach an LLM. It may consume a
	// small amount of provider quota, so callers must use it deliberately.
	Probe(ctx context.Context) Diagnostic
	Translate(ctx context.Context, req TranslationRequest) (TranslationResponse, error)
}

type Registry interface {
	Get(ProviderID) (Provider, bool)
	List() []Provider
}

type MapRegistry map[ProviderID]Provider

func NewRegistry(providers ...Provider) (MapRegistry, error) {
	registry := make(MapRegistry, len(providers))
	for _, provider := range providers {
		if provider == nil {
			return nil, fmt.Errorf("register provider: nil adapter")
		}
		id := provider.ID()
		if id == "" {
			return nil, fmt.Errorf("register provider: empty adapter ID")
		}
		if _, exists := registry[id]; exists {
			return nil, fmt.Errorf("register provider %q: duplicate adapter ID", id)
		}
		registry[id] = provider
	}
	return registry, nil
}

func (r MapRegistry) Get(id ProviderID) (Provider, bool) { p, ok := r[id]; return p, ok }
func (r MapRegistry) List() []Provider {
	out := make([]Provider, 0, len(r))
	for _, provider := range r {
		out = append(out, provider)
	}
	return out
}
