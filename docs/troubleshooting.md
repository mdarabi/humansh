# Troubleshooting

## Shell setup

Run:

```sh
humansh doctor
humansh doctor --fix
exec zsh
```

Run `humansh setup` to configure your login shell, or `humansh setup --advanced` to discover and configure every usable supported shell. Bash 4.3+ is required so Humansh can safely capture and restore existing Readline shell-command bindings. The `/bin/bash` shipped by macOS is 3.2, so select Zsh or install a current Bash and ensure it is the `bash` found in `PATH`.

Setup updates startup files, but it cannot change the already-running parent shell. If a natural-language line such as `list files` produces `zsh: command not found: list` immediately after installation, open a new terminal or run `exec zsh`; do not run the natural-language request as a literal command again until the new shell has loaded the managed block. If a new shell prints that the Humansh binary is missing, rerun the installer before using the integration.

Humansh cannot always modify `.zshrc` or `.bashrc`. Setup runs as the current user without `sudo`, requires owner-writable regular files, and atomically replaces them through writable parent directories. For a symlink, those checks apply to the resolved regular-file target. If the quick flow detects an access failure, it makes no changes and points to `humansh setup --no-shell-change`, which prints the exact block to add manually. It cannot be combined with an integration restriction that would leave an old managed block active.

Humansh is installed before `zsh-syntax-highlighting` so that plugin can wrap the widget. If the binary is moved or deleted, the widget fails open to the previous Enter binding and prints a one-time warning.

Run `humansh-bindings` inside Zsh to see the active and captured previous Enter, clear-line, and force widgets in `main`, `emacs`, `viins`, and `vicmd`. If `doctor` reports a later direct `bindkey` reset or Enter binding, move it before the humansh block (while keeping `zsh-syntax-highlighting` after humansh), then run `humansh setup --repair`.

In Bash, `humansh-bindings` reports the `emacs-standard`, `vi-insert`, and `vi-command` Readline maps. Ordinary Enter is deliberately unchanged. Type an English request and press Ctrl-G to translate it; review the replacement and press Enter to run low/medium-risk output. High-risk output requires Ctrl-X then Enter.

Escape clears the complete command line by default. In vi insert mode this replaces Escape's usual transition to command mode. Choose another clear-line shortcut with `humansh config set shell.clear_line_binding '^U'`; the previous Escape widget returns after opening a new shell, or immediately when `humansh-off` is safe to run.

Setup and uninstall preserve a symlinked `.zshrc` or `.bashrc` and atomically update its regular-file target. Setup resolves valid chains; uninstall fails closed with manual guidance for dangling or chained links instead of replacing them.

`doctor --fix` repairs humansh-owned file permissions, the immutable shell asset, and the managed block. A missing binary requires reinstalling:

```sh
./scripts/install.sh --local
```

When a valid manually formatted `config.toml` or `classifier.toml` must be rewritten, humansh first preserves its exact bytes in a mode-0600 sibling named `*.humansh-backup-*`.

`humansh uninstall` and the standalone uninstall script validate every machine-managed install-state path against the current XDG/home layout before deleting anything. If that preflight reports corrupt or redirected state, run `humansh doctor --fix` and retry. Use `humansh uninstall --purge` only when configuration and credentials should also be removed; declining cancels the entire operation. A successful child process cannot unload bindings already held by its parent shell; restart that shell only to replace its in-memory state.

## Codex

```sh
humansh provider test codex
```

Humansh uses `codex exec` and lets the selected Codex distribution manage authentication. It does not inspect login status or the local auth record. If the CLI rejects a mandatory structured-output or tool-disable setting, the full test shows that exact bounded error; update or reconfigure the distribution rather than weakening isolation.

## Claude Code

Shell aliases can hide multiple installations. Automatic selection uses the first executable named `claude` in `PATH`, then falls back to the native installer's `~/.local/bin/claude` path so a new/self-updated CLI is still found before the shell refreshes PATH. `humansh setup --advanced` lists distinct PATH installations and lets you pin the one whose provider-managed inference works. You can also change it directly:

```sh
humansh config set providers.claude.binary /absolute/path/to/claude
humansh config set providers.claude.binary auto
```

Run `humansh provider test claude`. Humansh uses `claude -p` for its minimal live check and does not invoke `claude auth` subcommands. If a corporate distribution says login is disabled but normal print mode works, that is supported. Follow that distribution's own authentication procedure only when the inference command itself fails.

Known parent-shell API keys, auth tokens, custom base URLs, and Bedrock/Vertex/Foundry selectors are not inherited by the isolated Claude subprocess. Required production flags still fail closed if the selected distribution does not implement them.

## Cursor CLI

Humansh prefers `cursor-agent`, falls back to `agent`, and does not use the `cursor` editor launcher. Setup can pin a specific installation when multiple distinct CLI executables are present. You can also select it directly:

```sh
humansh config set providers.cursor.binary /absolute/path/to/cursor-agent
humansh config set providers.cursor.binary auto
```

Test the complete structured invocation with:

```sh
humansh provider test cursor
```

Humansh treats Cursor authentication as provider-managed and does not call its login/status commands. Known parent-shell API/auth/endpoint overrides are not inherited. If the selected distribution rejects read-only Ask mode, sandboxing, trust, or JSON output during the full test, update or reconfigure it rather than weakening those controls.

If Cursor's inference check fails, quick setup prints Cursor's own bounded, credential-redacted message without trying to classify it or invent recovery steps. Fix the issue described by Cursor, then run the installer again. A fresh stopped installation removes its staged Humansh binary; an upgrade restores the previous binary. The stop is handled cleanly, so `make install` does not add a `make: *** ... Error 22` line.

## OpenRouter

```sh
humansh provider configure openrouter --model provider/model
humansh provider test openrouter
```

HTTP 401 means the key is missing/invalid; 402 means insufficient credits or key spending limit; 403 is a permission/policy denial; 404 is an invalid model; 429 is rate limiting. OpenRouter is never used as silent paid fallback.

Do not set only `providers.openrouter.model` and expect it to become usable. The configure command performs read-only key and model-capability checks before automatically running the disclosed minimal metered schema check, then records the successful concrete model. A model can exist on OpenRouter and support basic `response_format` while lacking the `structured_outputs` capability humansh requires; choose one from `https://openrouter.ai/models?order=newest&supported_parameters=structured_outputs`. If a setup candidate is rejected, paste the next model ID directly at the repeated model prompt; type `back` to choose another provider.

## Classification

Inspect a surprising result without contacting a provider:

```sh
print -rn -- 'INPUT' | humansh classify --first-token-kind unknown
humansh classifier list
```

Use `Ctrl-G` to force translation, or press `Ctrl-X` then `Enter` to run the exact text unchanged. If you customize either binding, humansh shows the configured key sequence in its next-step messages. Conflicting overrides are reported by `humansh doctor`.

Short plain-language requests use active-shell resolution rather than a verb allowlist. Any input containing at least two plain words and no shell syntax, such as `list files` or `summarize logs`, should translate when `whence -w` reports that its first word is not found. If the first word resolves as an executable, alias, function, builtin, or reserved word, normal command classification applies. A likely typo such as `gti status` therefore enters translation for review; the generated command is never executed automatically.

## Provider output

Exit 25 means the provider returned an incomplete/malformed final response; retry or run `humansh provider test`. Exit 26 means local policy rejected content such as controls, Markdown, surrounding prose, or obfuscated execution. In both cases the original buffer and cursor remain unchanged and nothing executes.

Syntax failures are exit 25. Terminal controls, presentation prompts, Markdown, alternatives/prose, and rejected obfuscation are exit 26. High-risk but reviewable commands return exit 14 and remain gated in ZLE or Readline.
