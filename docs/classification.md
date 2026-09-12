# Classification

`humansh` classifies intent, not shell syntax. English such as `find all files modified today` is syntactically a command invocation, so parsing alone cannot decide whether Enter should execute it.

The local classifier has three outcomes:

- `literal`: strong command evidence with weak English evidence. Zsh delegates to the prior Enter widget; Bash exposes the same result to machine callers while ordinary Enter remains native Readline behavior.
- `natural_language`: strong English evidence with weak command evidence. The selected provider is called and its command is inserted for review.
- `ambiguous`: conflicting or insufficient evidence. The buffer remains unchanged; no provider is called.

The thresholds are command `>= 5`, English `>= 5`, and weak conflict `<= 2`. Strong evidence on both sides is always ambiguous. A structural mismatch against parseable help from the installed command is also ambiguous, even when its English score is weak; this prevents an undocumented word or option from being executed or translated automatically. If help is unavailable or cannot be parsed, the classifier falls back to its conservative lexical evidence instead of pretending the invocation is invalid.

Before scoring, empty input, multiline buffers, and leading comments take the hard literal routes `empty_input`, `multiline_input`, and `leading_comment`. Exact command and English-prefix overrides take `configured_command_override` and `configured_english_prefix`; a conflict takes `conflicting_user_overrides`. These route labels are zero-weight decision evidence. The score-based `DecisionCode` values are `strong_command_weak_english`, `strong_english_weak_command`, `conflicting_strong_evidence`, and `insufficient_evidence`; `command_grammar_uncertain` is the separate fail-closed grammar veto.

## Command evidence

| Code | Weight | Normative trigger/example |
|---|---:|---|
| `resolved_first_token` | 5 | The active shell resolves the head as alias/function/builtin/reserved/command: `git status`. |
| `shell_operator` | 5 | Unquoted operator: `cat file | grep error`. |
| `explicit_command_path` | 5 | Head begins `/`, `./`, `../`, or `~/`: `./scripts/test`. |
| `assignment_prefix` | 5 | Leading shell assignment: `FOO=bar make`. |
| `shell_control_construct` | 5 | Control head such as `if`, `for`, `while`, `case`, or `function`. |
| `command_or_process_substitution` | 4 | Unquoted `$(...)`, backticks, `<(...)`, or `>(...)`. |
| `parameter_expansion` | 3 | Unquoted `$name` or `${name}`. |
| `conventional_flag` | 3 | Unquoted `-x` or `--long`: `ls -lah`. |
| `glob_syntax` | 3 | Unquoted glob syntax: `print **/*.go`; a sentence-ending `?` is excluded. |
| `quoted_argument` | 2 | Quoted argument after a resolved/path head: `echo 'show me files'`. |
| `path_argument` | 2 | Path-like argument: `cat ./README.md`. |
| `command_grammar_recognized` | 0 | Parsed help from the installed executable recognized the structured invocation. |
| `command_grammar_partial` | 0 | Parsed help exposed useful but explicitly incomplete structure; the remaining tail stayed inspectable. |
| `command_grammar_undocumented_subcommand` | 0 | Parsed help did not document the next word as a subcommand. |
| `command_grammar_unknown_option` | 0 | Parsed help did not document an option at the current command node. |
| `command_grammar_missing_option_value` | 0 | A known option that requires a value was the final word. |
| `command_grammar_dynamic_word` | 0 | A quoted or expansion-bearing word prevents safe help-guided traversal. |
| `command_grammar_help_unavailable` | 0 | A documented prefix was matched, but its deeper help could not be loaded. |
| `command_grammar_help_unparseable` | 0 | A documented prefix was matched, but its deeper help had no supported structure. |
| `command_grammar_depth_limit` | 0 | Traversal stopped at the fixed recursion or probe-count bound. |

A `smart` or `classify` caller supplies the fixed first-token kind—alias, function, builtin, reserved, command, unresolved, empty, or unknown—and may supply the exact external executable path selected by its active shell. Zsh Smart Enter does so without running the command. The installed Bash UI uses explicit force-translation rather than classification, while direct `readline-v1 smart` callers reach the same Go classifier and command-grammar interface as `zle-v1`. The typed line stays on stdin. Resolution is evidence, never an unconditional verdict; help parsing and grammar analysis do not live in a shell script.

### Installed-command help analysis

Classifier result version 2 can include a raw-free `command_grammar` summary with source `installed_help`. Production has no Git-specific schema, static command catalog, or command-name branch. The analyzer asks the exact resolved executable for its current help, parses common usage/commands/options forms, and walks global options, documented subcommands, nested subcommands, node options, option values, and `--`. Each input word receives an internal role such as subcommand, option, option value, positional, or unexpected. Ambiguous brace-enumerated argument choices are never promoted to executable subcommands unless they occur inside an explicit command section, and attached/separate option-value forms are honored as advertised. The parser also combines evidence across wrapped synopsis lines to decode corroborated BSD/POSIX compact groups such as `[-dIPRrvWx]`, while exact option rows, word-like single-dash options, and attached-value forms are not split into invented flags.

Positional, unexpected, and forwarded words remain eligible for English-tail evidence. Documented option values—including free-form values such as `git commit -m "please authenticate"`—are excluded. Operands stay inspectable because help syntax cannot determine intent: a command may accept arbitrary operands, so a grammatically possible invocation can still be an English request.

A bounded synopsis such as `tool run [OPTIONS] TARGET [COMMAND] [ARG...]` also establishes a forwarded command tail. The analyzer validates leading options and their values, then marks the operand tail as forwarded with partial coverage. This keeps `docker run IMAGE curl --silent` from checking curl's flags against Docker's option list. Forwarded flag spellings are excluded from English-tail inspection, but their following words are still inspected: no option-value arity is inferred, and `docker run node curl --help is failing please authenticate` stays ambiguous. The forwarded command is never help-probed. Inference requires complete help, a required operand before `COMMAND`, a repeated argument tail, and one synopsis with no listed subcommands. Sibling invocation forms remain separate from more-indented wrapped syntax. A quoted or dynamic word where leading-option parsing stops cannot establish the operand boundary and keeps the line ambiguous; an explicit `--` can establish that boundary. Column-delimited option definitions retain custom lowercase value types, so `--network network` consumes its value before the operand boundary.

```text
git status                                      → literal
git --no-pager status --short                   → literal
git commit -m "please authenticate"             → literal
git is failing please authenticate              → ambiguous
git status is failing please authenticate       → ambiguous
git status --porcelian                          → ambiguous
git -C                                          → ambiguous
```

These Git rows are examples of the generic mechanism, not production special cases, and depend on the installed Git's help. The same analyzer works with any external executable whose help exposes recognizable structure. A word absent from an explicitly exhaustive command list is uncertain; absence from a list labeled “common,” “basic,” or otherwise partial falls back to inspectable operands instead. That distinction lets an unlisted extension such as `git remote` remain runnable while `git is failing please authenticate` is still caught by English-tail evidence. Aliases, functions, builtins, reserved words, and unresolved heads keep the conservative lexical behavior. A standalone CLI invocation may resolve a simple external head with `exec.LookPath`, but it never starts an interactive shell or sources startup files.

Each help probe directly invokes the resolved path as `<executable> [documented-subcommand ...] --help`. Only subcommand words recognized by prior help are carried into a deeper probe; the unrecognized user tail is never passed to the executable. A tightly bounded syntax sentence such as `Use "tool help <topic>"` can establish that the typed positional-help form is command-like, but that word and its tail remain inspectable operands and are never used for another probe. Humansh never directly tries `-h`, positional `help`, `man`, completion scripts, a shell wrapper, or shell startup files. Probes receive EOF stdin, a minimal environment, isolated temporary home and working directories, bounded output, a short timeout, process-group cancellation, and a fixed recursion/probe-count limit.

This is not a sandbox. An executable can ignore `--help`, load plugins or other programs, access the network, daemonize, or perform side effects. Humansh therefore probes only an executable the shell resolved, but users must still trust that executable's help behavior. The typed line and generated commands are never executed. Stop reasons include `undocumented_subcommand`, `unknown_option`, `missing_option_value`, `dynamic_shell_word`, `help_unavailable`, `help_unparseable`, and `depth_limit`. Help that is unavailable or unparseable falls back conservatively; it is not proof that the typed invocation is invalid. Reaching the traversal-depth limit fails closed to `ambiguous` because the remaining structure could not be checked. Raw help output is neither returned nor persisted, and replacing or upgrading the executable is observed on the next classification.

## English evidence

| Code | Weight | Normative trigger/example |
|---|---:|---|
| `natural_instruction_prefix` | 5 | Fixed grammatical request prefixes such as `show me`, `please`, or `help me`. |
| `natural_question_prefix` | 5 | Fixed question prefixes, or grammatical `which … is/are/uses/using/listening`. |
| `question_mark` | 3 | Sentence-ending `?` that is not part of a glob. |
| `unresolved_plain_phrase` | 3 | The active shell definitively reports an unresolved head and the input contains at least two plain alphabetic words with no shell markers. |
| `ordinary_sentence_structure` | 3 | Four or more ordinary words, an instruction/unresolved head, and no shell markers. |
| `natural_language_tail` | 4 | An inspectable resolved-command tail has a `grammar-tail-v1` word and no tail shell markers: `find all files modified today` or `git is failing please authenticate`. |
| `natural_clause` | 3 | A fixed clause such as `modified today`, gated by instruction/unresolved/tail evidence. |
| `unresolved_first_token` | 2 | The active shell reports `unresolved` or `unknown`; this alone never forces translation. |
| `mostly_ordinary_words` | 2 | A structurally English row whose tail is at least 75% alphabetic ordinary words. |
| `stopword_or_pronoun_density` | 1 | A structurally English row with at least two lexicon/stop words. |

The resolved-command tail lexicon is versioned as `grammar-tail-v1`:

```text
a all an any each every no some the this that these those whatever whichever
he her hers him his i it its me mine my our ours she their theirs them they us we what which who whom whose you your yours
am are be been being can could did do does had has have is may might must shall should was were will would
about after at before by during for from if in into of on over through to under until with without
```

There is no command-name negative list. For an external command with usable help, token roles determine which words remain inspectable. For aliases, functions, builtins, reserved words, unresolved heads, and help that is unavailable or unparseable, the classifier uses the same lexical mechanism without hard-coding knowledge of any command. The corpus proves that `all` makes `find all files modified today` ambiguous and `it` makes `make it faster` ambiguous.

For short plain phrases, active-shell resolution is decisive without being verb-specific. `list files`, `summarize logs`, `gti status`, and any other two-or-more-word alphabetic phrase enter translation when Zsh definitively reports the head as unresolved. A resolved `list files` is literal unless it independently carries strong English evidence. Unknown resolution, single words, and inputs containing shell syntax remain conservative. This intentionally sends likely command typos to translation, where the suggested replacement is inserted for review but never executed automatically. Exact command overrides remain available for commands handled outside normal shell resolution.

### When a real command's operands look like English

The tail rule is grammatical, not a list of known-chatty commands. That is what lets it catch `watch the logs` and `top processes by memory`, which no fixed command list would have covered. The cost is that a genuine command whose operands happen to be lexicon words is also ambiguous:

```text
mv a b               → ambiguous   ('a' is a determiner)
cp a b               → ambiguous
touch a b            → ambiguous
make all install     → ambiguous   ('all' is a quantifier)
time make all        → ambiguous
who am i             → ambiguous   ('am', 'i')
nano my notes        → ambiguous   ('my')
```

This is the safe direction, and deliberately so. A resolved head always scores command `5`, so these rows can never reach `natural_language`, which requires a command score of `2` or less — a real command is never silently rewritten or sent to a provider. The typed line is preserved and is not executed; only the bounded help probes described above may have run. Press the force-literal binding to execute the line as typed.

If a command you run often lands here, add it as an exact override so it is always literal:

```sh
print -rn -- 'nano' | humansh classifier add-command
```

Force-translate still works on overridden commands, so nothing is lost. These rows are pinned in the corpus, so a change to the lexicon that moves any of them shows up as a test failure rather than as a surprise in someone's terminal.

The built-in clause fragments are `is using`, `are using`, `that were`, `in this folder`, `from the last`, `modified today`, `changed today`, `changed during`, `by size`, `by memory`, `is listening`, `were running`, `to the`, `if the`, and `it faster`. Prefix, clause, and tail lists use whole-word matching. Both score domains are additive; command `>=5` plus English `>=5` always fails toward `ambiguous`, even when one score is larger.

Use `humansh classify --json` to see scores, the raw-free command-grammar summary, primary decision code, and ordered evidence. The structure may change when the resolved executable or its help changes; Humansh deliberately does not cache a command catalog across classifications. Raw input and raw help are omitted from output. The LLM never breaks a tie or authorizes execution.

Overrides live in `${XDG_CONFIG_HOME:-$HOME/.config}/humansh/classifier.toml`. Exact command overrides are case-sensitive; English prefixes are case-insensitive and whitespace-normalized. Changing weights, lexicon entries, help-parser behavior, or normative examples requires corpus fixtures and release notes.

Add and remove overrides only through stdin:

```sh
print -rn -- 'deploy' | humansh classifier add-command
print -rn -- 'deploy' | humansh classifier remove-command
print -rn -- 'explain how to' | humansh classifier add-english-prefix
print -rn -- 'explain how to' | humansh classifier remove-english-prefix
```
