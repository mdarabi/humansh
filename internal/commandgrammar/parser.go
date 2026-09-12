package commandgrammar

import (
	"bytes"
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxHelpParseBytes = 256 << 10

var (
	ansiCSI       = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	ansiOSC       = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)
	optionNameRE  = regexp.MustCompile(`--?[A-Za-z0-9][A-Za-z0-9_-]*`)
	negatedOptRE  = regexp.MustCompile(`--\[no-\]([A-Za-z0-9][A-Za-z0-9_-]*)`)
	commandRowRE  = regexp.MustCompile(`^\s{2,}([A-Za-z0-9][A-Za-z0-9._+-]*(?:\s*,\s*[A-Za-z0-9][A-Za-z0-9._+-]*)*)\s{2,}\S`)
	braceChoiceRE = regexp.MustCompile(`\{([A-Za-z0-9._+-]+(?:\s*,\s*[A-Za-z0-9._+-]+)+)\}`)
	commandMetaRE = regexp.MustCompile(`(?:^|[^A-Za-z0-9_])(?:SUB)?COMMANDS?(?:$|[^A-Za-z0-9_])`)
	optionTypeRE  = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
)

var metavarWords = map[string]struct{}{
	"arg": {}, "args": {}, "bool": {}, "command": {}, "count": {}, "dir": {}, "directory": {},
	"duration": {}, "file": {}, "float": {}, "int": {}, "name": {}, "number": {}, "path": {},
	"string": {}, "text": {}, "uint": {}, "value": {}, "values": {},
}

// ParseHelp normalizes common help/manual layouts into a generic command node.
// It extracts syntax only from usage, command, and option regions; prose is not
// treated as grammar.
func ParseHelp(data []byte, complete bool) (NodeSpec, error) {
	if len(data) == 0 || len(data) > maxHelpParseBytes || bytes.IndexByte(data, 0) >= 0 {
		return NodeSpec{}, errors.New("help output is empty, binary, or too large")
	}
	if !utf8.Valid(data) {
		return NodeSpec{}, errors.New("help output is not UTF-8")
	}
	text := normalizeHelp(string(data))
	if strings.TrimSpace(text) == "" {
		return NodeSpec{}, errors.New("help output is empty after normalization")
	}

	node := NodeSpec{
		Options:             make(map[string]OptionSpec),
		Subcommands:         make(map[string]struct{}),
		UnprobedSubcommands: make(map[string]struct{}),
		SubcommandsComplete: false,
		Complete:            complete,
	}
	var commandSection, optionSection, synopsisSection, usageContinuation bool
	var sawStructure, sawUsage, sawCommandMarker, sawOptionSection, sawOpaqueOptions bool
	var usageLines []string
	var usageForms [][]string
	var synopsisIndent int
	var synopsisHead string
	synopsisBreak := true
	declaredOptions := make(map[string]OptionSpec)
	shortOptionDiagnostic := rejectsLongHelpOption(text)
	type commandCandidate struct {
		indent int
		names  []string
	}
	var commandCandidates []commandCandidate
	flushCommandCandidates := func() {
		if len(commandCandidates) == 0 {
			return
		}
		commandIndent := commandCandidates[0].indent
		for _, candidate := range commandCandidates[1:] {
			if candidate.indent < commandIndent {
				commandIndent = candidate.indent
			}
		}
		for _, candidate := range commandCandidates {
			if candidate.indent != commandIndent {
				continue
			}
			for _, name := range candidate.names {
				node.Subcommands[name] = struct{}{}
			}
		}
		commandCandidates = nil
	}
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if trimmed == "" {
			usageContinuation = false
			synopsisBreak = true
			continue
		}
		if subcommand, ok := documentedPositionalHelpForm(trimmed); ok {
			node.Subcommands[subcommand] = struct{}{}
			node.UnprobedSubcommands[subcommand] = struct{}{}
			sawStructure = true
		}

		if isCommandHeader(trimmed) {
			flushCommandCandidates()
			commandSection, optionSection, synopsisSection, usageContinuation = true, false, false, false
			sawStructure, sawCommandMarker = true, true
			node.SubcommandsComplete = node.SubcommandsComplete || completeCommandHeader(trimmed)
			continue
		}
		if isPositionalHeader(trimmed) {
			flushCommandCandidates()
			commandSection, optionSection, synopsisSection, usageContinuation = false, false, false, false
			sawStructure, node.AcceptsPositionals = true, true
			continue
		}
		if isOptionHeader(trimmed) {
			flushCommandCandidates()
			commandSection, optionSection, synopsisSection, usageContinuation = false, true, false, false
			node.OptionsKnown, sawStructure, sawOptionSection = true, true, true
			continue
		}
		if isSynopsisHeader(trimmed) {
			flushCommandCandidates()
			commandSection, optionSection, synopsisSection, usageContinuation = false, false, true, false
			sawStructure, sawUsage = true, true
			synopsisBreak = true
			continue
		}
		if isSectionHeader(trimmed) {
			flushCommandCandidates()
			commandSection, optionSection, synopsisSection, usageContinuation = false, false, false, false
		}

		usageLine := strings.HasPrefix(lower, "usage:") || strings.HasPrefix(lower, "usage ")
		continuedUsage := usageContinuation && len(line) > 0 && unicode.IsSpace(rune(line[0]))
		if usageLine {
			usageContinuation = true
		} else if usageContinuation && !continuedUsage {
			usageContinuation = false
		}
		if usageLine || continuedUsage || synopsisSection {
			sawStructure, sawUsage, node.OptionsKnown = true, true, true
			usageLines = append(usageLines, line)
			// Keep distinct invocation forms separate. Only more-indented
			// syntax atoms can wrap a form; a repeated command head, another
			// usage label, or a sibling line starts a new synopsis.
			body := trimmed
			if usageLine {
				body = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(body[len("usage"):]), ":"))
			}
			if fields := strings.Fields(body); len(fields) > 0 {
				first := fields[0]
				indent := commandIndent(line)
				canWrap := strings.HasPrefix(first, "[") || strings.HasPrefix(first, "<") || strings.HasPrefix(first, "-") || first == strings.ToUpper(first)
				if usageLine || synopsisBreak || indent <= synopsisIndent || first == synopsisHead || !canWrap {
					usageForms = append(usageForms, []string{body})
					synopsisIndent, synopsisHead = indent, first
				} else {
					last := len(usageForms) - 1
					usageForms[last] = append(usageForms[last], body)
				}
				synopsisBreak = false
			}
			parseUsageOptions(line, node.Options)
			sawOpaqueOptions = sawOpaqueOptions || hasOpaqueOptionGroup(line)
			if containsCommandMarker(line) || hasPositionalBraceChoice(line) {
				sawCommandMarker = true
			}
		}

		if (optionSection || sawUsage) && indentedOptionLine(line) {
			definition := optionDefinition(line)
			parsed := parseOptionGroupWithPipeAliases(definition, true, definition != strings.TrimSpace(line))
			mergeOptions(node.Options, parsed)
			// An indented option spelling outside a bracketed usage atom is an
			// exact declaration even when a terse help page omits both an Options
			// header and descriptive prose.
			mergeOptions(declaredOptions, parsed)
		}
		if commandSection {
			names := append(commandRow(line), isolatedBraceNames(line)...)
			if len(names) > 0 {
				commandCandidates = append(commandCandidates, commandCandidate{indent: commandIndent(line), names: names})
			}
		}
	}
	flushCommandCandidates()
	mergeOptions(node.Options, inferCompactUsageOptions(usageLines, node.Options, declaredOptions, shortOptionDiagnostic))
	if sawOpaqueOptions && !sawOptionSection && len(declaredOptions) == 0 {
		node.OptionsKnown = false
	}

	if len(node.Subcommands) > 0 {
		node.SubcommandState = SubcommandsListed
	} else if sawCommandMarker {
		node.SubcommandState = SubcommandsUnknown
	} else if sawUsage {
		node.SubcommandState = SubcommandsNone
	} else {
		node.SubcommandState = SubcommandsUnknown
	}
	if complete && len(node.Subcommands) == 0 && len(usageForms) == 1 {
		node.ForwardsCommand = hasForwardedCommandTail(usageForms[0])
	}
	if !sawStructure || len(node.Options) == 0 && len(node.Subcommands) == 0 && !sawUsage {
		return NodeSpec{}, errors.New("no supported help structure found")
	}
	return node, nil
}

// hasForwardedCommandTail recognizes a bounded synopsis such as
// "tool run [OPTIONS] TARGET [COMMAND] [ARG...]". An explicit required operand
// distinguishes a forwarded command from a tool's own COMMAND subcommands.
// Alternatives, optional operands, and options after operands are deliberately
// not inferred. The caller must establish that these lines belong to one form.
func hasForwardedCommandTail(lines []string) bool {
	fields := strings.Fields(strings.Join(lines, " "))
	if len(fields) > 0 && strings.EqualFold(fields[0], "usage:") {
		fields = fields[1:]
	}
	if len(fields) < 5 {
		return false
	}
	switch strings.ToLower(fields[len(fields)-2]) {
	case "command", "[command]", "<command>", "[<command>]":
	default:
		return false
	}
	switch strings.ToLower(fields[len(fields)-1]) {
	case "[arg...]", "[args...]", "[arg]...", "[args]...", "[<arg>...]", "[<args>...]", "[<arg>]...", "[<args>]...":
	default:
		return false
	}
	options := -1
	for index, field := range fields[:len(fields)-2] {
		switch strings.ToLower(field) {
		case "[options]", "[flags]", "[<options>]", "[<flags>]":
			options = index
		}
	}
	if options < 1 || options >= len(fields)-3 {
		return false
	}
	for _, field := range fields[:options] {
		if !validCommandName(field) {
			return false
		}
	}
	for _, field := range fields[options+1 : len(fields)-2] {
		if strings.HasPrefix(field, "<") && strings.HasSuffix(field, ">") {
			field = field[1 : len(field)-1]
		} else if field != strings.ToUpper(field) {
			return false
		}
		if !validCommandName(field) || strings.Contains(field, ".") || containsCommandMarker(field) {
			return false
		}
	}
	return true
}

func commandIndent(line string) int {
	indent := 0
	for _, r := range line {
		if !unicode.IsSpace(r) {
			break
		}
		indent++
	}
	return indent
}

func indentedOptionLine(line string) bool {
	if len(line) == 0 || !unicode.IsSpace(rune(line[0])) {
		return false
	}
	return strings.HasPrefix(strings.TrimLeftFunc(line, unicode.IsSpace), "-")
}

func normalizeHelp(value string) string {
	value = ansiOSC.ReplaceAllString(value, "")
	value = ansiCSI.ReplaceAllString(value, "")
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	runes := make([]rune, 0, len(value))
	for _, r := range value {
		switch r {
		case '\b':
			if len(runes) > 0 {
				runes = runes[:len(runes)-1]
			}
		case '\t':
			runes = append(runes, ' ', ' ', ' ', ' ')
		default:
			if r == '\n' || r >= 0x20 {
				runes = append(runes, r)
			}
		}
	}
	return string(runes)
}

func isCommandHeader(value string) bool {
	trimmed := strings.TrimSpace(value)
	lower := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(trimmed, ":")))
	if !strings.Contains(lower, "command") || len(lower) > 100 || strings.ContainsAny(lower, "[]<>") {
		return false
	}
	switch lower {
	case "commands", "available commands", "subcommands", "all commands", "supported commands", "built-in commands":
		return true
	}
	if !strings.HasSuffix(trimmed, ":") {
		return false
	}
	if strings.HasPrefix(lower, "these are common ") && containsWord(lower, "commands") || lower == "the commands are" {
		return true
	}
	heading := lower
	if open := strings.IndexByte(heading, '('); open >= 0 {
		if !strings.HasSuffix(heading, ")") {
			return false
		}
		heading = strings.TrimSpace(heading[:open])
	}
	fields := strings.Fields(heading)
	return len(fields) == 2 && fields[1] == "commands"
}

func completeCommandHeader(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(value), ":")))
	switch lower {
	case "commands", "available commands", "subcommands", "all commands", "the commands are", "supported commands":
		return true
	default:
		return false
	}
}

func isOptionHeader(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(value, ":")))
	switch lower {
	case "options", "option", "flags", "global options", "global flags", "optional arguments":
		return true
	default:
		return false
	}
}

func isPositionalHeader(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(value, ":")))
	switch lower {
	case "arguments", "positional arguments", "operands":
		return true
	default:
		return false
	}
}

func isSynopsisHeader(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(value, ":")))
	return lower == "synopsis" || lower == "usage"
}

func isSectionHeader(value string) bool {
	raw := strings.TrimSpace(value)
	colonTerminated := strings.HasSuffix(raw, ":")
	trimmed := strings.TrimSpace(strings.TrimSuffix(raw, ":"))
	if len(trimmed) == 0 || len(trimmed) > 60 || strings.ContainsAny(trimmed, "[]<>") {
		return false
	}
	letters, upper := 0, 0
	for _, r := range trimmed {
		if unicode.IsLetter(r) {
			letters++
			if unicode.IsUpper(r) {
				upper++
			}
		}
	}
	return letters > 0 && (letters == upper || colonTerminated && sectionTitle(trimmed))
}

func sectionTitle(value string) bool {
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r) || strings.ContainsRune("&()/_.-", r) {
			continue
		}
		return false
	}
	return true
}

func containsWord(value, target string) bool {
	for _, field := range strings.FieldsFunc(value, func(r rune) bool { return !unicode.IsLetter(r) }) {
		if field == target {
			return true
		}
	}
	return false
}

func containsCommandMarker(value string) bool {
	if commandMetaRE.MatchString(value) {
		return true
	}
	lower := strings.ToLower(value)
	for _, marker := range []string{"<command>", "[command]", "<commands>", "[commands]", " subcommand", " command ["} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// documentedPositionalHelpForm accepts only a tightly bounded conventional
// syntax sentence such as `Use "tool help <command>" ...`. The help word is
// retained for classification, but is marked unprobed so it can never become
// `tool help --help` in the runtime analyzer.
func documentedPositionalHelpForm(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if len(value) < len(`Use "x"`) || !strings.EqualFold(value[:4], "use ") {
		return "", false
	}
	quote := value[4]
	if quote != '\'' && quote != '"' {
		return "", false
	}
	remainder := value[5:]
	end := strings.IndexByte(remainder, quote)
	if end < 0 {
		return "", false
	}
	fields := strings.Fields(remainder[:end])
	if len(fields) < 3 || !validCommandName(fields[0]) || fields[1] != "help" {
		return "", false
	}
	for _, operand := range fields[2:] {
		if !helpFormMetavariable(operand) {
			return "", false
		}
	}
	return fields[1], true
}

func helpFormMetavariable(value string) bool {
	value = strings.TrimSuffix(value, "...")
	for len(value) >= 2 && (value[0] == '[' && value[len(value)-1] == ']' || value[0] == '(' && value[len(value)-1] == ')') {
		value = value[1 : len(value)-1]
	}
	if len(value) >= 3 && value[0] == '<' && value[len(value)-1] == '>' {
		return validCommandName(value[1 : len(value)-1])
	}
	if strings.EqualFold(value, "topic") || strings.EqualFold(value, "topics") {
		return true
	}
	return beginsMetavar(value)
}

func commandRow(line string) []string {
	if len(line) == 0 || !unicode.IsSpace(rune(line[0])) {
		return nil
	}
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return nil
	}
	if !strings.ContainsAny(trimmed, " \t") {
		return validCommandList(strings.TrimSuffix(trimmed, ","))
	}
	if strings.Contains(trimmed, ",") && !strings.Contains(trimmed, "  ") && !strings.Contains(trimmed, "\t") {
		return validCommandList(strings.TrimSuffix(trimmed, ","))
	}
	match := commandRowRE.FindStringSubmatch(line)
	if len(match) != 2 {
		return nil
	}
	return validCommandList(match[1])
}

func validCommandList(value string) []string {
	var out []string
	for _, candidate := range strings.Split(value, ",") {
		candidate = strings.TrimSpace(candidate)
		if validCommandName(candidate) {
			out = append(out, candidate)
		} else {
			return nil
		}
	}
	return out
}

func validCommandName(value string) bool {
	if value == "" || strings.HasPrefix(value, "-") {
		return false
	}
	for _, r := range value {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("._+-", r) {
			return false
		}
	}
	return true
}

// Braced choices are ambiguous in usage text: argparse uses the same spelling
// for subcommands and ordinary positional/option enums. They are therefore a
// marker of unknown positional structure, never proof that a word is safe to
// pass back to the executable. Names are accepted only inside an explicit
// command section.
func hasPositionalBraceChoice(line string) bool {
	for _, match := range braceChoiceRE.FindAllStringSubmatchIndex(line, -1) {
		if braceIsOptionValue(line[:match[0]]) {
			continue
		}
		return true
	}
	return false
}

func braceIsOptionValue(prefix string) bool {
	fields := strings.Fields(prefix)
	if len(fields) == 0 {
		return false
	}
	previous := fields[len(fields)-1]
	if strings.HasSuffix(previous, "]") || strings.HasSuffix(previous, ")") || strings.HasSuffix(previous, "}") {
		return false
	}
	previous = strings.TrimLeft(previous, "[({")
	return strings.HasPrefix(previous, "-")
}

// isolatedBraceNames accepts a brace list only when it is the entire indented
// command row. A brace enum in a command description is an operand domain, not
// evidence that any enum member is safe to pass to a nested help probe.
func isolatedBraceNames(line string) []string {
	if len(line) == 0 || !unicode.IsSpace(rune(line[0])) {
		return nil
	}
	trimmed := strings.TrimSpace(line)
	match := braceChoiceRE.FindStringSubmatch(trimmed)
	if len(match) != 2 || match[0] != trimmed {
		return nil
	}
	return validCommandList(match[1])
}

func parseUsageOptions(line string, destination map[string]OptionSpec) {
	groups := bracketGroups(line)
	if len(groups) == 0 {
		mergeOptions(destination, parseOptionGroupWithPipeAliases(line, false, false))
		return
	}
	for _, group := range groups {
		if strings.Contains(group, "-") {
			mergeOptions(destination, parseOptionGroupWithPipeAliases(group, false, false))
		}
	}
}

// hasOpaqueOptionGroup identifies usage atoms such as [OPTIONS], [global
// flags], and Go's [build/test flags]. Without a corresponding Options/Flags
// section, those placeholders explicitly say that the synopsis omitted the
// accepted spellings, so absence from the parsed option map is inconclusive
// unless exact option-definition rows appear elsewhere in the help output.
func hasOpaqueOptionGroup(value string) bool {
	for _, group := range bracketGroups(value) {
		// An atom containing a dash may instead declare an exact option whose
		// value happens to be named FLAGS, as in [--flags FLAGS].
		if strings.Contains(group, "-") {
			continue
		}
		lower := strings.ToLower(group)
		for _, marker := range []string{"option", "options", "flag", "flags"} {
			if containsWord(lower, marker) {
				return true
			}
		}
	}
	return false
}

type usageOptionAtom struct {
	body     string
	terminal bool
}

// inferCompactUsageOptions is deliberately a second pass. BSD usage output
// often wraps corroborating singleton options onto another synopsis line, and
// an exact single-dash option documented in an option row must win over a
// cluster-shaped spelling in Usage. The exact multi-character spelling remains
// in the option map; this pass only adds supported one-byte members.
func inferCompactUsageOptions(lines []string, parsed, declared map[string]OptionSpec, rejectedLongHelp bool) map[string]OptionSpec {
	options := make(map[string]OptionSpec)
	knownShort := make(map[byte]struct{})
	for name := range parsed {
		if len(name) == 2 && name[0] == '-' && name[1] != '-' {
			knownShort[name[1]] = struct{}{}
		}
	}
	shortOptionDialect := rejectedLongHelp || len(knownShort) >= 2
	for _, line := range lines {
		for _, group := range bracketGroups(line) {
			atoms := usageOptionAtoms(group)
			isolatedAlternative := strings.Contains(group, "|") && hasSingletonUsageAtom(atoms)
			for _, atom := range atoms {
				name := "-" + atom.body
				if len(atom.body) < 2 || !atom.terminal {
					continue
				}
				if _, protected := declared[name]; protected {
					continue
				}
				sharesKnown := false
				for index := 0; index < len(atom.body); index++ {
					if _, exists := knownShort[atom.body[index]]; exists {
						sharesKnown = true
						break
					}
				}
				if !compactOptionBody(atom.body, shortOptionDialect, rejectedLongHelp, isolatedAlternative, sharesKnown) {
					continue
				}
				options[name] = OptionSpec{}
				for index := 0; index < len(atom.body); index++ {
					options["-"+string(atom.body[index])] = OptionSpec{}
				}
			}
		}
	}
	return options
}

func usageOptionAtoms(group string) []usageOptionAtom {
	var atoms []usageOptionAtom
	for index := 0; index+1 < len(group); {
		if group[index] != '-' || group[index+1] == '-' || !usageAtomBoundary(group, index-1) {
			index++
			continue
		}
		end := index + 1
		for end < len(group) && compactOptionCharacter(group[end]) {
			end++
		}
		if end == index+1 {
			index++
			continue
		}
		after := end
		for after < len(group) && (group[after] == ' ' || group[after] == '\t') {
			after++
		}
		terminal := after == len(group) || group[after] == ']' || group[after] == '|'
		atoms = append(atoms, usageOptionAtom{body: group[index+1 : end], terminal: terminal})
		index = end
	}
	return atoms
}

func hasSingletonUsageAtom(atoms []usageOptionAtom) bool {
	for _, atom := range atoms {
		if len(atom.body) == 1 {
			return true
		}
	}
	return false
}

func usageAtomBoundary(value string, index int) bool {
	if index < 0 || index >= len(value) {
		return true
	}
	switch value[index] {
	case '[', ']', '|', ' ', '\t':
		return true
	default:
		return false
	}
}

func compactOptionCharacter(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || strings.ContainsRune("@%?,", rune(value))
}

func compactOptionBody(body string, shortOptionDialect, rejectedLongHelp, isolatedAlternative, sharesKnown bool) bool {
	if len(body) < 2 || beginsMetavar(body[1:]) {
		return false
	}
	seen := make(map[byte]struct{}, len(body))
	lower, upper, digits, symbols := 0, 0, 0, 0
	for index := 0; index < len(body); index++ {
		value := body[index]
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
		switch {
		case value >= 'a' && value <= 'z':
			lower++
		case value >= 'A' && value <= 'Z':
			upper++
		case value >= '0' && value <= '9':
			digits++
		default:
			symbols++
		}
	}
	if len(body) <= 4 {
		return digits == 0 && symbols == 0 && (rejectedLongHelp || isolatedAlternative || sharesKnown)
	}
	if symbols > 0 {
		return lower+upper >= 2 && (shortOptionDialect || len(body) >= 8)
	}
	if digits > 0 {
		// A rejected --help probe is strong evidence for a traditional
		// single-byte option dialect. Long mixed sets can include digit flags
		// (for example, -4 and -6), while short -O2/-j4 spellings remain
		// attached-value candidates and are excluded above.
		return rejectedLongHelp && len(body) >= 5 && lower > 0 && upper > 0
	}
	return len(body) >= 5 && lower > 0 && upper > 0 && shortOptionDialect
}

func rejectsLongHelpOption(value string) bool {
	lower := strings.ToLower(value)
	return strings.Contains(lower, "illegal option -- -") || strings.Contains(lower, "unrecognized option") && strings.Contains(lower, "--help")
}

func bracketGroups(value string) []string {
	var out []string
	depth, start := 0, -1
	for index, r := range value {
		switch r {
		case '[':
			if depth == 0 {
				start = index
			}
			depth++
		case ']':
			if depth == 0 {
				continue
			}
			depth--
			if depth == 0 && start >= 0 {
				out = append(out, value[start:index+1])
				start = -1
			}
		}
	}
	return out
}

func optionDefinition(line string) string {
	trimmed := strings.TrimSpace(line)
	for index := 0; index+1 < len(trimmed); index++ {
		if trimmed[index] != '\t' && (trimmed[index] != ' ' || trimmed[index+1] != ' ') {
			continue
		}
		end := index
		for end < len(trimmed) && (trimmed[end] == ' ' || trimmed[end] == '\t') {
			end++
		}
		remainder := strings.TrimSpace(trimmed[end:])
		if remainder == "" || strings.HasPrefix(remainder, "-") || beginsMetavar(remainder) {
			index = end - 1
			continue
		}
		return strings.TrimSpace(trimmed[:index])
	}
	return trimmed
}

func parseOptionGroupWithPipeAliases(definition string, sharePipeValues, typedValues bool) map[string]OptionSpec {
	out := make(map[string]OptionSpec)
	definition = negatedOptRE.ReplaceAllString(definition, "--$1, --no-$1")
	matches := optionMatches(definition)
	if len(matches) == 0 {
		return out
	}
	// Commas join aliases. Pipes do so in option-definition rows, but inside a
	// usage group they separate alternatives: `-a | -o FILE` must not make -a
	// consume a value merely because -o does.
	groupMode := strings.Contains(definition, ",") || sharePipeValues && strings.Contains(definition, "|")
	groupValue := NoValue
	groupSeparate, groupAttached := false, false
	for index, match := range matches {
		name := definition[match[0]:match[1]]
		end := len(definition)
		if index+1 < len(matches) {
			end = matches[index+1][0]
		}
		fragment := definition[match[1]:end]
		spec := optionFromFragment(name, fragment, typedValues)
		out[name] = mergeOption(out[name], spec)
		if spec.Value > groupValue {
			groupValue = spec.Value
		}
		groupSeparate = groupSeparate || spec.AllowSeparate
		groupAttached = groupAttached || spec.AllowAttached
	}
	if groupMode && groupValue != NoValue {
		for name, spec := range out {
			if spec.Value == NoValue {
				spec.Value = groupValue
				spec.AllowSeparate = groupSeparate
				if strings.HasPrefix(name, "-") && !strings.HasPrefix(name, "--") {
					spec.AllowAttached = groupAttached
				}
				out[name] = spec
			}
		}
	}
	if _, hasHelp := out["--help"]; hasHelp {
		if short, ok := out["-h"]; ok {
			short.Terminal = true
			out["-h"] = short
		}
	}
	return out
}

func optionMatches(definition string) [][]int {
	raw := optionNameRE.FindAllStringIndex(definition, -1)
	out := make([][]int, 0, len(raw))
	for _, match := range raw {
		if match[0] > 0 {
			before := rune(definition[match[0]-1])
			if !unicode.IsSpace(before) && !strings.ContainsRune("[({,|/", before) {
				continue
			}
		}
		out = append(out, match)
	}
	return out
}

func optionFromFragment(name, fragment string, typedValues bool) OptionSpec {
	spec := OptionSpec{Terminal: name == "--help" || name == "--version"}
	fragment = strings.TrimRight(fragment, " \t,|)")
	if fragment == "" {
		return spec
	}
	withoutSeparators := strings.TrimLeft(fragment, ",|")
	separate := len(withoutSeparators) > 0 && (withoutSeparators[0] == ' ' || withoutSeparators[0] == '\t')
	trimmed := strings.TrimLeft(withoutSeparators, " \t")
	optional := strings.HasPrefix(trimmed, "[")
	attached := strings.HasPrefix(trimmed, "=") || strings.HasPrefix(trimmed, "[=")
	if attached {
		spec.AllowAttached = true
		if optional {
			spec.Value = OptionalValue
		} else {
			spec.Value = RequiredValue
		}
		return spec
	}
	// A column-delimited option definition can advertise arbitrary lowercase
	// types (for example "--network network  Connect ..."). Do not use that
	// inference in a synopsis or an undelimited prose description.
	if separate && (beginsMetavar(trimmed) || typedValues && optionTypeRE.MatchString(trimmed)) {
		spec.AllowSeparate = true
		if optional {
			spec.Value = OptionalValue
		} else {
			spec.Value = RequiredValue
		}
		return spec
	}
	if !separate && strings.HasPrefix(name, "-") && !strings.HasPrefix(name, "--") && beginsMetavar(trimmed) {
		spec.AllowAttached = true
		if optional {
			spec.Value = OptionalValue
		} else {
			spec.Value = RequiredValue
		}
	}
	return spec
}

func beginsMetavar(value string) bool {
	value = strings.TrimLeft(value, "[,=(")
	if value == "" {
		return false
	}
	if value[0] == '<' {
		return true
	}
	field := strings.Trim(strings.Fields(value)[0], "[]<>{},=()")
	if field == "" {
		return false
	}
	if field == strings.ToUpper(field) && strings.IndexFunc(field, unicode.IsLetter) >= 0 {
		return true
	}
	_, ok := metavarWords[strings.ToLower(field)]
	return ok
}

func mergeOptions(destination, source map[string]OptionSpec) {
	for name, spec := range source {
		destination[name] = mergeOption(destination[name], spec)
	}
}

func mergeOption(left, right OptionSpec) OptionSpec {
	if right.Value > left.Value {
		left.Value = right.Value
	}
	left.AllowSeparate = left.AllowSeparate || right.AllowSeparate
	left.AllowAttached = left.AllowAttached || right.AllowAttached
	left.Terminal = left.Terminal || right.Terminal
	return left
}
