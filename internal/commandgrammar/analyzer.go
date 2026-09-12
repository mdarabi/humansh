package commandgrammar

import (
	"context"
	"strings"
	"time"
)

const (
	defaultMaxHelpDepth  = 4
	defaultMaxHelpProbes = 4
	defaultAnalysisTime  = 900 * time.Millisecond
)

type HelpAnalyzer struct {
	source    HelpSource
	maxDepth  int
	maxProbes int
	timeout   time.Duration
}

func NewAnalyzer(source HelpSource) *HelpAnalyzer {
	return &HelpAnalyzer{source: source, maxDepth: defaultMaxHelpDepth, maxProbes: defaultMaxHelpProbes, timeout: defaultAnalysisTime}
}

func (a *HelpAnalyzer) Analyze(ctx context.Context, inv Invocation) Analysis {
	if a == nil || a.source == nil || len(inv.Words) == 0 || !inv.Words[0].Static || inv.Words[0].Quoted {
		return unmodeled(len(inv.Words))
	}
	analysisCtx, cancel := context.WithTimeout(ctx, a.timeLimit())
	defer cancel()
	session, err := a.source.Open(analysisCtx, ExecutableRef{Head: inv.Words[0].Text, Path: inv.ExecutablePath})
	if err != nil {
		return unmodeled(len(inv.Words))
	}
	defer session.Close()

	root := session.Load(analysisCtx, nil)
	if root.Status != HelpOK {
		return unmodeled(len(inv.Words))
	}
	analysis := Analysis{
		Source:      "installed_help",
		Coverage:    CoverageRecognized,
		StopReason:  StopComplete,
		Boundary:    len(inv.Words),
		HelpDepth:   1,
		Annotations: annotations(len(inv.Words)),
	}
	analysis.Annotations[0].Role = RoleHead
	if !root.Node.Complete {
		analysis.Coverage = CoveragePartial
	}

	node := root.Node
	prefix := make([]string, 0, a.depthLimit())
	index := 1
	for {
		var terminal, stopped bool
		index, terminal, stopped = consumeLeadingOptions(inv.Words, index, node, &analysis)
		if stopped {
			return finish(analysis, index)
		}
		if terminal {
			markRemainder(&analysis, index, RoleUnexpected)
			analysis.Boundary = index
			return finish(analysis, index)
		}

		switch node.SubcommandState {
		case SubcommandsListed:
			if index == len(inv.Words) {
				return finish(analysis, index)
			}
			word := inv.Words[index]
			if !word.Static || word.Quoted {
				return stopAt(analysis, index, CoverageIndeterminate, StopDynamicShellWord)
			}
			if _, ok := node.Subcommands[word.Text]; !ok {
				if strings.HasPrefix(word.Text, "-") {
					return stopAt(analysis, index, CoverageIndeterminate, StopUnknownOption)
				}
				if node.AcceptsPositionals {
					return consumeLeaf(inv.Words, index, node, analysis)
				}
				if !node.Complete || !node.SubcommandsComplete {
					markRemainder(&analysis, index, RolePositional)
					analysis.Coverage = CoveragePartial
					analysis.Boundary = index
					return finish(analysis, index)
				}
				return stopAt(analysis, index, CoverageIndeterminate, StopUndocumentedSubcommand)
			}
			if _, unprobed := node.UnprobedSubcommands[word.Text]; unprobed {
				markRemainder(&analysis, index, RolePositional)
				analysis.Coverage = CoveragePartial
				analysis.Boundary = index
				return finish(analysis, len(inv.Words))
			}
			analysis.Annotations[index].Role = RoleSubcommand
			prefix = append(prefix, word.Text)
			index++
			if index == len(inv.Words) {
				return finish(analysis, index)
			}
			if len(prefix) >= a.depthLimit() || analysis.HelpDepth >= a.probeLimit() {
				return stopAt(analysis, index, CoveragePartial, StopDepthLimit)
			}
			loaded := session.Load(analysisCtx, prefix)
			analysis.HelpDepth++
			if loaded.Status != HelpOK {
				markRemainder(&analysis, index, RolePositional)
				analysis.Coverage = CoveragePartial
				analysis.Boundary = index
				if loaded.Status == HelpUnparseable {
					analysis.StopReason = StopHelpUnparseable
				} else {
					analysis.StopReason = StopHelpUnavailable
				}
				return finish(analysis, index)
			}
			node = loaded.Node
			if !node.Complete {
				analysis.Coverage = CoveragePartial
			}
		default:
			return consumeLeaf(inv.Words, index, node, analysis)
		}
	}
}

func (a *HelpAnalyzer) probeLimit() int {
	if a.maxProbes <= 0 {
		return defaultMaxHelpProbes
	}
	return a.maxProbes
}

func (a *HelpAnalyzer) timeLimit() time.Duration {
	if a.timeout <= 0 {
		return defaultAnalysisTime
	}
	return a.timeout
}

func (a *HelpAnalyzer) depthLimit() int {
	if a.maxDepth <= 0 {
		return defaultMaxHelpDepth
	}
	return a.maxDepth
}

func consumeLeadingOptions(words []Word, index int, node NodeSpec, analysis *Analysis) (next int, terminal, stopped bool) {
	for index < len(words) {
		word := words[index]
		if !word.Static || word.Quoted || !strings.HasPrefix(word.Text, "-") || word.Text == "-" {
			return index, false, false
		}
		if word.Text == "--" {
			analysis.Annotations[index].Role = RoleOption
			index++
			if node.ForwardsCommand {
				*analysis = forwardTail(*analysis, index)
				return index, false, true
			}
			markRemainder(analysis, index, RolePositional)
			return len(words), false, true
		}
		next, terminal, ok := consumeOption(words, index, node, analysis)
		if !ok {
			if !node.OptionsKnown || !node.Complete {
				analysis.Annotations[index].Role = RoleOption
				markRemainder(analysis, index+1, RolePositional)
				analysis.Coverage = CoveragePartial
				analysis.Boundary = index
				return index, false, true
			}
			stoppedAnalysis := stopAt(*analysis, index, CoverageIndeterminate, StopUnknownOption)
			*analysis = stoppedAnalysis
			return index, false, true
		}
		index = next
		if analysis.Uncertain() {
			return index, false, true
		}
		if terminal {
			return index, true, false
		}
	}
	return index, false, false
}

func consumeLeaf(words []Word, index int, node NodeSpec, analysis Analysis) Analysis {
	if node.SubcommandState == SubcommandsUnknown || !node.Complete {
		analysis.Coverage = CoveragePartial
	}
	if node.ForwardsCommand && index < len(words) {
		// Leading-option parsing can also stop at an undecoded shell word. It
		// is not evidence of an operand: quotes/expansions could conceal flags.
		word := words[index]
		if !word.Static || word.Quoted || strings.ContainsAny(word.Text, "*?[]{}~") {
			return stopAt(analysis, index, CoverageIndeterminate, StopDynamicShellWord)
		}
		return forwardTail(analysis, index)
	}
	for index < len(words) {
		word := words[index]
		if word.Static && !word.Quoted && word.Text == "--" {
			analysis.Annotations[index].Role = RoleOption
			index++
			markRemainder(&analysis, index, RolePositional)
			return finish(analysis, len(words))
		}
		if word.Static && !word.Quoted && strings.HasPrefix(word.Text, "-") && word.Text != "-" {
			next, terminal, ok := consumeOption(words, index, node, &analysis)
			if ok {
				index = next
				if analysis.Uncertain() {
					return finish(analysis, index)
				}
				if terminal {
					markRemainder(&analysis, index, RoleUnexpected)
					analysis.Boundary = index
					return finish(analysis, index)
				}
				continue
			}
			if node.OptionsKnown && node.Complete {
				return stopAt(analysis, index, CoverageIndeterminate, StopUnknownOption)
			}
			analysis.Coverage = CoveragePartial
		}
		analysis.Annotations[index].Role = RolePositional
		index++
	}
	return finish(analysis, index)
}

func forwardTail(analysis Analysis, index int) Analysis {
	// Distinguish unvalidated forwarded words from ordinary operands so their
	// flag spellings cannot disable inspection of the remaining English tail.
	markRemainder(&analysis, index, RoleForwarded)
	analysis.Coverage = CoveragePartial
	analysis.Boundary = index
	return finish(analysis, index)
}

func consumeOption(words []Word, index int, node NodeSpec, analysis *Analysis) (next int, terminal, ok bool) {
	option, inlineValue, ok := lookupOption(node.Options, words[index].Text)
	if !ok {
		return index, false, false
	}
	analysis.Annotations[index].Role = RoleOption
	if inlineValue || option.Value == NoValue {
		return index + 1, option.Terminal, true
	}
	if option.Value == OptionalValue {
		if !option.AllowSeparate || index+1 >= len(words) || looksLikeOption(words[index+1]) {
			return index + 1, option.Terminal, true
		}
		if _, subcommand := node.Subcommands[words[index+1].Text]; subcommand {
			return index + 1, option.Terminal, true
		}
		analysis.Annotations[index+1].Role = RoleOptionValue
		return index + 2, option.Terminal, true
	}
	if !option.AllowSeparate || index+1 >= len(words) || looksLikeOption(words[index+1]) {
		analysis.Coverage = CoverageIndeterminate
		analysis.StopReason = StopMissingOptionValue
		analysis.Boundary = index + 1
		markRemainder(analysis, index+1, RoleUnexpected)
		return index + 1, false, true
	}
	analysis.Annotations[index+1].Role = RoleOptionValue
	return index + 2, option.Terminal, true
}

func lookupOption(options map[string]OptionSpec, word string) (OptionSpec, bool, bool) {
	if option, ok := options[word]; ok {
		return option, false, true
	}
	if strings.HasPrefix(word, "--") {
		if name, _, ok := strings.Cut(word, "="); ok {
			option, found := options[name]
			if found && option.Value != NoValue && option.AllowAttached {
				return option, true, true
			}
		}
		return OptionSpec{}, false, false
	}
	if strings.HasPrefix(word, "-") && len(word) > 2 {
		terminal := false
		for offset := 1; offset < len(word); offset++ {
			option, found := options["-"+string(word[offset])]
			if !found || option.Value != NoValue {
				if !found {
					return OptionSpec{}, false, false
				}
				option.Terminal = option.Terminal || terminal
				if offset+1 < len(word) {
					if !option.AllowAttached {
						return OptionSpec{}, false, false
					}
					return option, true, true
				}
				return option, false, true
			}
			terminal = terminal || option.Terminal
		}
		return OptionSpec{Terminal: terminal}, false, true
	}
	return OptionSpec{}, false, false
}

func looksLikeOption(word Word) bool {
	return word.Static && !word.Quoted && strings.HasPrefix(word.Text, "-") && word.Text != "-"
}

func stopAt(analysis Analysis, index int, coverage Coverage, reason StopReason) Analysis {
	markRemainder(&analysis, index, RoleUnexpected)
	analysis.Coverage = coverage
	analysis.StopReason = reason
	analysis.Boundary = index
	return finish(analysis, index)
}

func markRemainder(analysis *Analysis, start int, role Role) {
	for index := start; index < len(analysis.Annotations); index++ {
		analysis.Annotations[index].Role = role
	}
}

func finish(analysis Analysis, matched int) Analysis {
	analysis.Matched = matched
	if analysis.Boundary > len(analysis.Annotations) {
		analysis.Boundary = len(analysis.Annotations)
	}
	return analysis
}

func annotations(count int) []Annotation {
	values := make([]Annotation, count)
	for index := range values {
		values[index].Index = index
	}
	return values
}

func unmodeled(count int) Analysis {
	return Analysis{Coverage: CoverageUnmodeled, StopReason: StopComplete, Boundary: count, Annotations: annotations(count)}
}
