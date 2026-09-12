// Package commandgrammar discovers and analyzes the command-line grammar
// advertised by an installed executable's help output.
package commandgrammar

import "context"

// Analyzer finds the longest prefix supported by the installed executable.
// Implementations may inspect the executable with fixed help-only requests,
// but must never pass unrecognized invocation words to it.
type Analyzer interface {
	Analyze(context.Context, Invocation) Analysis
}

// Word is a shell word produced by a non-evaluating lexer. Text is useful only
// when Static is true. Quoted and dynamic words can still be consumed as known
// option values by position.
type Word struct {
	Text   string
	Static bool
	Quoted bool
}

type Invocation struct {
	Words          []Word
	ExecutablePath string
}

type Coverage string

const (
	CoverageUnmodeled     Coverage = "unmodeled"
	CoverageRecognized    Coverage = "recognized"
	CoveragePartial       Coverage = "partial"
	CoverageIndeterminate Coverage = "indeterminate"
)

type StopReason string

const (
	StopComplete               StopReason = "complete"
	StopUndocumentedSubcommand StopReason = "undocumented_subcommand"
	StopUnknownOption          StopReason = "unknown_option"
	StopMissingOptionValue     StopReason = "missing_option_value"
	StopDynamicShellWord       StopReason = "dynamic_shell_word"
	StopHelpUnavailable        StopReason = "help_unavailable"
	StopHelpUnparseable        StopReason = "help_unparseable"
	StopDepthLimit             StopReason = "depth_limit"
)

type Role string

const (
	RoleUnknown     Role = "unknown"
	RoleHead        Role = "head"
	RoleSubcommand  Role = "subcommand"
	RoleOption      Role = "option"
	RoleOptionValue Role = "option_value"
	RolePositional  Role = "positional"
	RoleUnexpected  Role = "unexpected"
)

type Annotation struct {
	Index int  `json:"index"`
	Role  Role `json:"role"`
}

// Analysis contains only structural metadata. It deliberately omits the raw
// invocation, executable path, help text, and token annotations from JSON.
type Analysis struct {
	Source      string       `json:"source,omitempty"`
	Coverage    Coverage     `json:"coverage"`
	StopReason  StopReason   `json:"stop_reason,omitempty"`
	Matched     int          `json:"matched_words"`
	Boundary    int          `json:"boundary"`
	HelpDepth   int          `json:"help_depth,omitempty"`
	Annotations []Annotation `json:"-"`
}

func (a Analysis) Modeled() bool { return a.Coverage != CoverageUnmodeled }

// Uncertain reports a structural mismatch that must keep a resolved command
// out of both automatic execution and automatic translation.
func (a Analysis) Uncertain() bool {
	switch a.StopReason {
	case StopUndocumentedSubcommand, StopUnknownOption, StopMissingOptionValue, StopDynamicShellWord, StopDepthLimit:
		return true
	default:
		return false
	}
}

func (a Analysis) RoleAt(index int) Role {
	if index < 0 || index >= len(a.Annotations) {
		return RoleUnknown
	}
	return a.Annotations[index].Role
}

type ValueMode uint8

const (
	NoValue ValueMode = iota
	RequiredValue
	OptionalValue
)

// OptionSpec records only value forms explicitly present in help output.
type OptionSpec struct {
	Value         ValueMode
	AllowSeparate bool
	AllowAttached bool
	Terminal      bool
}

type SubcommandState uint8

const (
	SubcommandsUnknown SubcommandState = iota
	SubcommandsNone
	SubcommandsListed
)

// NodeSpec is the normalized, command-agnostic result of one help probe.
type NodeSpec struct {
	Options      map[string]OptionSpec
	OptionsKnown bool
	Subcommands  map[string]struct{}
	// UnprobedSubcommands are advertised invocation forms whose word and tail
	// remain inspectable operands. They must never be used as recursive help
	// prefixes; positional help is deliberately outside the probe contract.
	UnprobedSubcommands map[string]struct{}
	SubcommandState     SubcommandState
	SubcommandsComplete bool
	AcceptsPositionals  bool
	// ForwardsCommand marks a synopsis with leading options, required operands,
	// and a trailing COMMAND [ARG...]. The operand tail is outside this node's
	// option grammar and must remain inspectable, never a recursive help probe.
	ForwardsCommand bool
	Complete        bool
}

// ExecutableRef names the statically decoded command and, when supplied by a
// shell integration, the exact external executable selected by that shell.
type ExecutableRef struct {
	Head string
	Path string
}

type HelpStatus uint8

const (
	HelpOK HelpStatus = iota
	HelpUnavailable
	HelpUnparseable
)

type HelpResult struct {
	Node   NodeSpec
	Status HelpStatus
}

// HelpSource opens one executable for a bounded analysis. A session is used so
// recursive probes share the same resolved file and isolated environment.
type HelpSource interface {
	Open(context.Context, ExecutableRef) (HelpSession, error)
}

type HelpSession interface {
	Load(context.Context, []string) HelpResult
	Close() error
}
