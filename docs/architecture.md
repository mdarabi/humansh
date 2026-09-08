# Architecture

The product has four mandatory modules and one composition root:

```text
cmd/humansh → cli → bootstrap
                     ├─ app (provider/shell-neutral workflow)
                     ├─ classifier → commandgrammar help analyzer
                     ├─ contextinfo → injected host metadata
                     ├─ llm contract → codex | claude | cursor | openrouter
                     ├─ shell contract → zsh | bash
                     └─ config/setup
```

`app` classifies, selects an injected provider and shell adapter through registries, invokes injected local validation/risk services, receives privacy-normalized host context through an injected boundary, asks the shell adapter for no-execution syntax and executable-availability validation, and returns a protocol result. It imports no concrete adapter and performs no filesystem or subprocess discovery itself.

`cli` owns the Cobra command tree, fixed flag parsing, stdin/stdout protocol rendering, and exit-code presentation. Its handlers load the composition-root snapshot and delegate to app, config, setup, or diagnostics; they do not contain classification, provider, shell, or risk policy.

`llm` owns common request/response contracts. Each provider child package owns only non-inference discovery, its minimal live probe, transport, final structured-output extraction, and typed error mapping. It never classifies or manipulates a shell line editor.

`shell` owns capabilities, the target-shell prompt profile, installation diagnostics, and generated-command validation. The Zsh adapter embeds the ZLE asset and performs no-execution Zsh syntax checking. The Bash adapter embeds the Readline asset, requires Bash 4.3+, and validates with `bash --noprofile --norc -n`. Both then statically extract direct command words and pass only those names—not the generated line—to a fixed clean-shell resolver using the user's local `PATH`; candidate executables are never invoked. Parsing and availability detection are best effort so they cannot overrule the selected shell's real syntax result; dynamic command words and executables encoded as another command's arguments are not resolved. Neither adapter selects a provider. Portable `mvdan.cc/sh/v3` AST inspection also supplements—but cannot overrule—the selected shell's real syntax result for risk analysis.

`config` owns typed configuration, XDG paths, atomic persistence, credentials, install state, setup, repair, uninstall safety, and managed `.zshrc`/`.bashrc` rendering. Runtime modules receive one immutable snapshot. Quick setup selects the invoking Bash or Zsh session on a fresh install, falling back to `$SHELL` when it cannot identify that session. It preserves the recorded shell set on a healthy direct rerun, while an installer rerun adds its invoking shell to that set; advanced setup can discover every compatible shell. Both create read-only startup-file plans and verify them immediately before applying all changes in one rollback-protected transaction. An explicit `--shell` selection removes integrations outside the requested set in that same transaction. Config-plus-shell changes use save/apply/rollback so failed updates do not leave half-written TOML. Version-2 install state records each installed shell, protocol, startup path, asset path, and embedded-asset hash; doctor and uninstall independently revalidate every entry and remove the running binary last.

Version 1 is the first public configuration schema, so there is no pre-v1 migration. Unknown older or newer versions fail closed and are never rewritten in place; a future schema change must add an explicit tested migration before incrementing `CurrentVersion`.

Shell assets never parse TOML. Setup exports validated `HUMANSH_SMART_ENTER`, `HUMANSH_CLEAR_LINE_BINDING`, `HUMANSH_FORCE_TRANSLATE_BINDING`, and `HUMANSH_FORCE_LITERAL_BINDING` values immediately before sourcing the selected immutable asset. Zsh uses `zle-v1` and can route Smart Enter. Bash uses `readline-v1`, requires `smart_enter=false`, and invokes translation only from its force-translate callback. Asset hashes therefore remain stable when preferences change.

When Smart Enter is disabled in Zsh—or at all times in Bash—the prior Enter behavior remains installed during ordinary editing. Forced translation temporarily installs carriage-return and line-feed gates only for high-risk generated output. Completing review, force-literal, or clearing the line restores the prior bindings. Disabling humansh is refused while a high-risk generation is pending.

The clear-line callback defaults to Escape and empties only the live unsubmitted ZLE or Readline buffer. During translation, both shell integrations match the configured clear-line sequence directly, cancel the provider process tree, and consume the sequence before ZLE or Readline can replay it after the provider returns. Ctrl-C uses the same cancellation path but restores the original buffer. The callback resets cursor/selection state, pending risk state, message state, and any temporary Enter gate. It captures and restores the prior binding in each supported keymap. This intentionally replaces the vi-insert transition to command mode; longer Escape-prefixed sequences remain subject to the shell's normal key-sequence timeout.

Both integrations capture protocol stdout and stderr separately through a mode-0600 temporary channel. Only a non-empty single-line stdout value paired with a generated exit can replace the editable buffer; displayed stderr is sanitized and bounded.

The active shell lexes the first token without expansion and reports its fixed kind. For an external command it also resolves the exact executable path without running it; the full buffer remains on stdin. The Go classifier computes independent command and English scores and is the only authority for literal/natural/ambiguous intent.

The classifier owns an injected `commandgrammar.Analyzer`, shared by `zle-v1` and `readline-v1` smart/classify protocol calls. Zsh Smart Enter supplies its shell-resolved external path; Bash's installed explicit-translation callback does not classify or send an unused path. For a resolved external executable, the production analyzer invokes that exact path directly with a fixed `--help` argument. It may then repeat the probe with only subcommand words already documented by earlier help output. The analyzer parses common usage, command, and option forms, annotates the input words, and returns only a raw-free structural summary to the classifier. It contains no command-specific schemas or command-name branches. Help failures and formats it cannot parse fall back conservatively.

Runtime help inspection never passes the unrecognized input tail to the executable, never uses a shell, and never directly runs `man`, completion code, positional `help`, or speculative `-h` forms. Each probe and the overall traversal have fixed timeout, output, and depth/count bounds; they run with EOF stdin, a minimal environment, and isolated temporary home and working directories. This limits exposure but is not a sandbox: an executable can ignore `--help`, launch other programs, access the network, daemonize, or perform side effects. Neither the typed line nor a generated command is executed by this path.

To add a provider: implement `llm.Provider`; keep auth, transport, and typed error mapping in its child package; run the shared provider contract; then register it in `bootstrap` and add its typed config. To add a shell: implement `shell.Adapter`; declare capabilities; add a no-execution validator and integration asset where needed; run the shared shell contract; then register it in `bootstrap` and add typed shell config. Neither extension changes the app workflow.

Configuration flow is `FileStore.Load → Validate → bootstrap constructors → RuntimeConfig snapshot → app request`. Provider adapters receive only typed subconfiguration and injected runners/key loaders. The composition root also injects `contextinfo.Local`, which resolves only the privacy-normalized working-directory label outside the app workflow. No `PATH` or installed-command inventory enters the provider request. Shell settings flow separately through the safely quoted managed block. Secrets never enter `RuntimeConfig`: only `credential_ref` does, while the key loader reads Keychain/environment/the mode-0600 fallback at the provider boundary.

`scripts/check-architecture.sh` enforces the important forbidden imports in CI.
