package classifier

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/agenticlab-ai/humansh/internal/commandgrammar"
	"github.com/agenticlab-ai/humansh/internal/config"
	"github.com/agenticlab-ai/humansh/internal/shell"
)

const resultVersion = 2

type Classification string

const (
	Literal   Classification = "literal"
	Natural   Classification = "natural_language"
	Ambiguous Classification = "ambiguous"
)

type EvidenceDomain string

const (
	CommandEvidence  EvidenceDomain = "command"
	EnglishEvidence  EvidenceDomain = "english"
	DecisionEvidence EvidenceDomain = "decision"
)

type Evidence struct {
	Domain EvidenceDomain `json:"domain"`
	Code   string         `json:"code"`
	Weight int            `json:"weight"`
	Detail string         `json:"detail,omitempty"`
}

type Input struct {
	Raw                 string
	Shell               string
	FirstTokenKind      shell.FirstTokenKind
	ResolvedCommandPath string
	Overrides           config.ClassifierOverrides
}

type Result struct {
	Version        int                      `json:"version"`
	FirstTokenKind shell.FirstTokenKind     `json:"first_token_kind,omitempty"`
	Outcome        Classification           `json:"outcome"`
	CommandScore   int                      `json:"command_score"`
	EnglishScore   int                      `json:"english_score"`
	DecisionCode   string                   `json:"decision_code"`
	CommandGrammar *commandgrammar.Analysis `json:"command_grammar,omitempty"`
	Evidence       []Evidence               `json:"evidence"`
}

// InvocationAnalyzer is the shared command-grammar boundary used regardless of
// whether input arrived through the Zsh or Bash protocol.
type InvocationAnalyzer interface {
	Analyze(context.Context, commandgrammar.Invocation) commandgrammar.Analysis
}

type Classifier struct {
	Invocations InvocationAnalyzer
}

var (
	assignmentRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	flagRE       = regexp.MustCompile(`^--?[A-Za-z0-9]`)
	fileExtRE    = regexp.MustCompile(`(?i)^[A-Za-z0-9_.-]+\.[A-Za-z0-9]{1,10}$`)
)

var instructionPrefixes = []string{
	"show me", "tell me", "please", "help me", "can you", "could you", "i want to", "find me",
}

var questionPrefixes = []string{"how do i", "what is", "what are", "where is"}

var grammarLexicon = wordSet(`
a all an any each every no some the this that these those whatever whichever
he her hers him his i it its me mine my our ours she their theirs them they us we what which who whom whose you your yours
am are be been being can could did do does had has have is may might must shall should was were will would
about after at before by during for from if in into of on over through to under until with without
`)

var stopwords = wordSet(`the a an my me those these this that is are was were in during of to from by for it all`)

var naturalClauses = []string{
	"is using", "are using", "that were", "in this folder", "from the last", "modified today", "changed today",
	"changed during", "by size", "by memory", "is listening", "were running", "to the", "if the", "it faster",
}

func wordSet(words string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, word := range strings.Fields(words) {
		out[word] = struct{}{}
	}
	return out
}

func (c Classifier) Classify(in Input) Result {
	return c.ClassifyContext(context.Background(), in)
}

func (c Classifier) ClassifyContext(ctx context.Context, in Input) Result {
	raw := in.Raw
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return hard(in.FirstTokenKind, Literal, "empty_input")
	}
	if strings.ContainsAny(raw, "\r\n") {
		return hard(in.FirstTokenKind, Literal, "multiline_input")
	}
	if strings.HasPrefix(strings.TrimLeftFunc(raw, unicode.IsSpace), "#") {
		return hard(in.FirstTokenKind, Literal, "leading_comment")
	}

	scan := scanLine(raw)
	first := ""
	if len(scan.tokens) > 0 {
		first = scan.tokens[0].text
	}
	commandOverride := containsExact(in.Overrides.AlwaysCommands, first)
	englishOverride := matchesPrefix(in.Overrides.AlwaysNaturalLanguagePrefixes, normalizeWords(trimmed))
	if commandOverride && englishOverride {
		return hard(in.FirstTokenKind, Ambiguous, "conflicting_user_overrides")
	}
	if commandOverride {
		return hard(in.FirstTokenKind, Literal, "configured_command_override")
	}
	if englishOverride {
		return hard(in.FirstTokenKind, Natural, "configured_english_prefix")
	}

	grammar := c.analyzeInvocation(ctx, in, scan)

	var commandEvidence, englishEvidence []Evidence
	add := func(dst *[]Evidence, domain EvidenceDomain, code string, weight int, detail string) {
		*dst = append(*dst, Evidence{Domain: domain, Code: code, Weight: weight, Detail: detail})
	}
	if resolved(in.FirstTokenKind) {
		add(&commandEvidence, CommandEvidence, "resolved_first_token", 5, "first token resolves in the active shell")
	}
	if scan.shellOperator {
		add(&commandEvidence, CommandEvidence, "shell_operator", 5, "contains an unquoted shell operator")
	}
	if explicitCommandPath(first) {
		add(&commandEvidence, CommandEvidence, "explicit_command_path", 5, "command begins with an explicit path")
	}
	if scan.assignmentPrefix {
		add(&commandEvidence, CommandEvidence, "assignment_prefix", 5, "contains a shell assignment prefix")
	}
	if shellControl(first) {
		add(&commandEvidence, CommandEvidence, "shell_control_construct", 5, "begins with a shell control construct")
	}
	if scan.commandSubstitution {
		add(&commandEvidence, CommandEvidence, "command_or_process_substitution", 4, "contains command or process substitution")
	}
	if scan.parameterExpansion {
		add(&commandEvidence, CommandEvidence, "parameter_expansion", 3, "contains parameter expansion")
	}
	if scan.flag {
		add(&commandEvidence, CommandEvidence, "conventional_flag", 3, "contains a conventional command flag")
	}
	if scan.glob {
		add(&commandEvidence, CommandEvidence, "glob_syntax", 3, "contains unquoted glob syntax")
	}
	if scan.quotedArgument && (resolved(in.FirstTokenKind) || explicitCommandPath(first)) {
		add(&commandEvidence, CommandEvidence, "quoted_argument", 2, "contains a quoted command argument")
	}
	if scan.pathArgument {
		add(&commandEvidence, CommandEvidence, "path_argument", 2, "contains a path-like argument")
	}
	if grammar != nil {
		code, detail := grammarEvidence(*grammar)
		add(&commandEvidence, CommandEvidence, code, 0, detail)
	}

	normalized := normalizeWords(trimmed)
	words := ordinaryWords(scan.tokens)
	instruction := hasAnyPrefix(normalized, instructionPrefixes)
	question := hasAnyPrefix(normalized, questionPrefixes) || grammaticalWhich(normalized)
	if instruction {
		add(&englishEvidence, EnglishEvidence, "natural_instruction_prefix", 5, "begins with an explicit natural-language request")
	}
	if question {
		add(&englishEvidence, EnglishEvidence, "natural_question_prefix", 5, "begins with a grammatical question")
	}
	if strings.HasSuffix(strings.TrimSpace(trimmed), "?") && !scan.questionGlob {
		add(&englishEvidence, EnglishEvidence, "question_mark", 3, "ends with sentence punctuation")
	}
	noShellMarkers := !scan.shellOperator && !scan.flag && !scan.pathArgument && !scan.assignmentPrefix && !scan.commandSubstitution && !scan.parameterExpansion && !scan.glob && !scan.containsEquals
	plainUnresolvedPhrase := in.FirstTokenKind == shell.TokenUnresolved && len(scan.tokens) >= 2 && len(words) == len(scan.tokens) && noShellMarkers
	if plainUnresolvedPhrase {
		add(&englishEvidence, EnglishEvidence, "unresolved_plain_phrase", 3, "unresolved input contains multiple plain words and no shell syntax")
	}
	ordinaryStructure := (instruction || in.FirstTokenKind == shell.TokenUnresolved || in.FirstTokenKind == shell.TokenUnknown) && len(words) >= 4 && noShellMarkers
	if ordinaryStructure {
		add(&englishEvidence, EnglishEvidence, "ordinary_sentence_structure", 3, "contains at least four ordinary words in sentence order")
	}
	tailTokens := []token(nil)
	tailMarkersClear := noShellMarkers
	if grammar != nil {
		tailTokens = inspectableGrammarTail(scan.tokens, *grammar)
		tailMarkersClear = grammarTailHasNoShellMarkers(scan, tailTokens)
	} else if len(scan.tokens) > 1 {
		tailTokens = scan.tokens[1:]
	}
	tailAllowed := resolved(in.FirstTokenKind)
	tail := tailAllowed && grammarBearing(tailTokens) && tailMarkersClear
	if tail {
		detail := "resolved command is followed by a grammar-bearing English tail"
		if grammar != nil {
			detail = "installed command help leaves a grammar-bearing English tail"
		}
		add(&englishEvidence, EnglishEvidence, "natural_language_tail", 4, detail)
	}
	clause := containsClause(normalized) && (instruction || in.FirstTokenKind == shell.TokenUnresolved || in.FirstTokenKind == shell.TokenUnknown || tail)
	if clause {
		add(&englishEvidence, EnglishEvidence, "natural_clause", 3, "contains a natural-language clause")
	}
	if in.FirstTokenKind == shell.TokenUnresolved || in.FirstTokenKind == shell.TokenUnknown {
		add(&englishEvidence, EnglishEvidence, "unresolved_first_token", 2, "first token is unresolved in the active shell")
	}
	structural := instruction || question || plainUnresolvedPhrase || ordinaryStructure || tail || clause
	ordinaryTail := []token(nil)
	if len(scan.tokens) > 1 {
		ordinaryTail = scan.tokens[1:]
	}
	ordinaryMarkersClear := noShellMarkers
	if tail {
		ordinaryTail = tailTokens
		ordinaryMarkersClear = tailMarkersClear
	}
	if structural && mostlyOrdinaryWords(ordinaryTail) && ordinaryMarkersClear {
		add(&englishEvidence, EnglishEvidence, "mostly_ordinary_words", 2, "tail is predominantly ordinary alphabetic words")
	}
	densityWords := words
	if tail && grammar != nil {
		densityWords = ordinaryWords(tailTokens)
	}
	if structural && stopwordCount(densityWords) >= 2 {
		add(&englishEvidence, EnglishEvidence, "stopword_or_pronoun_density", 1, "contains multiple English function words")
	}

	result := Result{Version: resultVersion, FirstTokenKind: in.FirstTokenKind, CommandGrammar: grammar, Evidence: append(commandEvidence, englishEvidence...)}
	for _, evidence := range commandEvidence {
		result.CommandScore += evidence.Weight
	}
	for _, evidence := range englishEvidence {
		result.EnglishScore += evidence.Weight
	}
	result.Outcome, result.DecisionCode = decide(result.CommandScore, result.EnglishScore)
	if grammar != nil && grammar.Uncertain() && resolved(in.FirstTokenKind) && result.Outcome != Ambiguous {
		result.Outcome = Ambiguous
		result.DecisionCode = "command_grammar_uncertain"
	}
	result.Evidence = append(result.Evidence, Evidence{Domain: DecisionEvidence, Code: result.DecisionCode})
	if resolved(in.FirstTokenKind) && result.EnglishScore >= 3 {
		result.Evidence = append(result.Evidence, Evidence{Domain: DecisionEvidence, Code: "known_command_with_natural_language_tail"})
	}
	if (in.FirstTokenKind == shell.TokenUnresolved || in.FirstTokenKind == shell.TokenUnknown) && result.CommandScore < 5 && result.EnglishScore < 5 && len(scan.tokens) <= 3 {
		result.Evidence = append(result.Evidence, Evidence{Domain: DecisionEvidence, Code: "unresolved_command_like_input"})
	}
	return result
}

func decide(commandScore, englishScore int) (Classification, string) {
	switch {
	case commandScore >= 5 && englishScore <= 2:
		return Literal, "strong_command_weak_english"
	case englishScore >= 5 && commandScore <= 2:
		return Natural, "strong_english_weak_command"
	case commandScore >= 5 && englishScore >= 5:
		return Ambiguous, "conflicting_strong_evidence"
	default:
		return Ambiguous, "insufficient_evidence"
	}
}

func hard(kind shell.FirstTokenKind, outcome Classification, code string) Result {
	_, decisionCode := decide(0, 0)
	return Result{Version: resultVersion, FirstTokenKind: kind, Outcome: outcome, DecisionCode: decisionCode, Evidence: []Evidence{{Domain: DecisionEvidence, Code: decisionCode}, {Domain: DecisionEvidence, Code: code}}}
}

func resolved(kind shell.FirstTokenKind) bool {
	switch kind {
	case shell.TokenAlias, shell.TokenFunction, shell.TokenBuiltin, shell.TokenReserved, shell.TokenCommand:
		return true
	default:
		return false
	}
}

func (c Classifier) analyzeInvocation(ctx context.Context, in Input, scan scanResult) *commandgrammar.Analysis {
	if in.FirstTokenKind != shell.TokenCommand || scan.shellOperator || scan.assignmentPrefix || len(scan.tokens) == 0 {
		return nil
	}
	analyzer := c.Invocations
	if analyzer == nil {
		return nil
	}
	words := make([]commandgrammar.Word, len(scan.tokens))
	for index, scanned := range scan.tokens {
		words[index] = commandgrammar.Word{
			Text:   strings.Trim(scanned.text, "'\""),
			Static: staticGrammarWord(scanned),
			Quoted: scanned.quoted,
		}
	}
	analysis := analyzer.Analyze(ctx, commandgrammar.Invocation{Words: words, ExecutablePath: in.ResolvedCommandPath})
	if !analysis.Modeled() {
		return nil
	}
	return &analysis
}

func staticGrammarWord(value token) bool {
	if value.quoted || value.text == "" {
		return false
	}
	return !strings.ContainsAny(value.text, "\\$`'\"")
}

func grammarEvidence(analysis commandgrammar.Analysis) (string, string) {
	detail := "matched grammar from installed command help"
	switch analysis.StopReason {
	case commandgrammar.StopUndocumentedSubcommand:
		return "command_grammar_undocumented_subcommand", detail + " until an undocumented subcommand"
	case commandgrammar.StopUnknownOption:
		return "command_grammar_unknown_option", detail + " until an unknown option"
	case commandgrammar.StopMissingOptionValue:
		return "command_grammar_missing_option_value", detail + " until an option missing its value"
	case commandgrammar.StopDynamicShellWord:
		return "command_grammar_dynamic_word", detail + " until a dynamic shell word"
	case commandgrammar.StopHelpUnavailable:
		return "command_grammar_help_unavailable", detail + "; deeper help was unavailable"
	case commandgrammar.StopHelpUnparseable:
		return "command_grammar_help_unparseable", detail + "; deeper help was not structurally parseable"
	case commandgrammar.StopDepthLimit:
		return "command_grammar_depth_limit", detail + " until the bounded help depth"
	default:
		if analysis.Coverage == commandgrammar.CoveragePartial {
			return "command_grammar_partial", detail + "; the advertised structure was explicitly incomplete"
		}
		return "command_grammar_recognized", detail
	}
}

func inspectableGrammarTail(tokens []token, analysis commandgrammar.Analysis) []token {
	out := make([]token, 0, len(tokens))
	for index, value := range tokens {
		switch analysis.RoleAt(index) {
		case commandgrammar.RolePositional, commandgrammar.RoleUnexpected:
			out = append(out, value)
		case commandgrammar.RoleForwarded:
			// A forwarded flag is not evidence that all later words are command
			// syntax. Its arity is unknown: omit only the flag spelling, never
			// assume that a following English word is an opaque option value.
			if !value.quoted && flagRE.MatchString(value.text) {
				continue
			}
			out = append(out, value)
		}
	}
	return out
}

func grammarTailHasNoShellMarkers(scan scanResult, tokens []token) bool {
	if scan.shellOperator || scan.assignmentPrefix || scan.commandSubstitution || scan.parameterExpansion || scan.glob {
		return false
	}
	for _, value := range tokens {
		if value.quoted {
			continue
		}
		clean := strings.Trim(value.text, "'\"")
		if flagRE.MatchString(clean) || strings.Contains(clean, "=") || strings.HasPrefix(clean, "~") || strings.Contains(clean, "/") || fileExtRE.MatchString(filepath.Base(clean)) {
			return false
		}
	}
	return true
}

func containsExact(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func matchesPrefix(prefixes []string, normalized string) bool {
	for _, prefix := range prefixes {
		p := normalizeWords(prefix)
		if normalized == p || strings.HasPrefix(normalized, p+" ") {
			return true
		}
	}
	return false
}

func normalizeWords(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func hasAnyPrefix(value string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if value == prefix || strings.HasPrefix(value, prefix+" ") {
			return true
		}
	}
	return false
}

func grammaticalWhich(value string) bool {
	if !strings.HasPrefix(value, "which ") {
		return false
	}
	return strings.Contains(value, " is ") || strings.Contains(value, " are ") || strings.Contains(value, " uses ") || strings.Contains(value, " using ") || strings.Contains(value, " listening ")
}

func setHas(set map[string]struct{}, value string) bool { _, ok := set[value]; return ok }

func explicitCommandPath(first string) bool {
	return strings.HasPrefix(first, "./") || strings.HasPrefix(first, "../") || strings.HasPrefix(first, "/") || strings.HasPrefix(first, "~/")
}

func shellControl(first string) bool {
	switch first {
	case "if", "for", "while", "until", "case", "repeat", "select", "function", "{", "(":
		return true
	default:
		return false
	}
}

func grammarBearing(tokens []token) bool {
	if len(tokens) < 2 {
		return false
	}
	ordinary, grammar := 0, false
	for _, token := range tokens {
		if token.quoted {
			continue
		}
		word := strings.ToLower(strings.Trim(token.text, ".,!?():"))
		if isAlpha(word) {
			ordinary++
			grammar = grammar || setHas(grammarLexicon, word)
		}
	}
	return ordinary >= 2 && grammar
}

func containsClause(normalized string) bool {
	words := strings.Fields(normalized)
	for _, clause := range naturalClauses {
		clauseWords := strings.Fields(clause)
		for start := 0; start+len(clauseWords) <= len(words); start++ {
			matched := true
			for offset, word := range clauseWords {
				if words[start+offset] != word {
					matched = false
					break
				}
			}
			if matched {
				return true
			}
		}
	}
	return false
}

func ordinaryWords(tokens []token) []string {
	out := make([]string, 0, len(tokens))
	for _, token := range tokens {
		word := strings.Trim(token.text, ".,!?():")
		if isAlpha(word) {
			out = append(out, strings.ToLower(word))
		}
	}
	return out
}

func mostlyOrdinaryWords(tokens []token) bool {
	if len(tokens) < 2 {
		return false
	}
	ordinary := 0
	for _, token := range tokens {
		if isAlpha(strings.Trim(token.text, ".,!?():")) {
			ordinary++
		}
	}
	return ordinary >= 2 && ordinary*4 >= len(tokens)*3
}

func stopwordCount(words []string) int {
	count := 0
	for _, word := range words {
		if setHas(stopwords, word) || setHas(grammarLexicon, word) {
			count++
		}
	}
	return count
}

func isAlpha(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return true
}

type token struct {
	text   string
	quoted bool
}

type scanResult struct {
	tokens              []token
	shellOperator       bool
	assignmentPrefix    bool
	commandSubstitution bool
	parameterExpansion  bool
	flag                bool
	glob                bool
	questionGlob        bool
	quotedArgument      bool
	pathArgument        bool
	containsEquals      bool
}

func scanLine(raw string) scanResult {
	var result scanResult
	var current strings.Builder
	quote := rune(0)
	escaped := false
	quotedToken := false
	flush := func() {
		if current.Len() == 0 {
			return
		}
		value := current.String()
		result.tokens = append(result.tokens, token{text: value, quoted: quotedToken})
		current.Reset()
		quotedToken = false
	}
	runes := []rune(raw)
	for i, r := range runes {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			current.WriteRune(r)
			continue
		}
		if quote != 0 {
			current.WriteRune(r)
			if r == quote {
				quote = 0
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			quotedToken = true
			current.WriteRune(r)
			continue
		}
		if unicode.IsSpace(r) {
			flush()
			continue
		}
		if strings.ContainsRune("|&;<>", r) {
			result.shellOperator = true
		}
		if r == '`' || (r == '$' && i+1 < len(runes) && runes[i+1] == '(') || ((r == '<' || r == '>') && i+1 < len(runes) && runes[i+1] == '(') {
			result.commandSubstitution = true
		}
		if r == '$' && i+1 < len(runes) && (runes[i+1] == '{' || unicode.IsLetter(runes[i+1]) || runes[i+1] == '_') {
			result.parameterExpansion = true
		}
		if r == '*' || r == '[' || r == ']' {
			result.glob = true
		}
		if r == '?' && hasNonSpaceAfter(runes, i) {
			result.glob = true
			result.questionGlob = true
		}
		if r == '=' {
			result.containsEquals = true
		}
		current.WriteRune(r)
	}
	flush()
	for i, token := range result.tokens {
		clean := strings.Trim(token.text, "'\"")
		if i == 0 && assignmentRE.MatchString(clean) {
			result.assignmentPrefix = true
		}
		if i > 0 && !token.quoted && flagRE.MatchString(clean) {
			result.flag = true
		}
		if i > 0 && token.quoted {
			result.quotedArgument = true
		}
		if i > 0 && (strings.HasPrefix(clean, "~") || strings.Contains(clean, "/") || fileExtRE.MatchString(filepath.Base(clean))) {
			result.pathArgument = true
		}
	}
	return result
}

func hasNonSpaceAfter(runes []rune, index int) bool {
	for _, r := range runes[index+1:] {
		if !unicode.IsSpace(r) {
			return true
		}
	}
	return false
}
