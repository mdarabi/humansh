# Setup

`humansh setup` uses a short default path and keeps detailed customization behind `humansh setup --advanced`. A fresh default setup asks at most two questions: which installed provider to use when there is a real choice, and whether to apply the summarized startup-file change. Nothing is written before confirmation.

## Installing

### From a release

```sh
curl -fsSL https://raw.githubusercontent.com/agenticlab-ai/humansh/main/scripts/install.sh | sh
```

The installer downloads the matching binary from the latest GitHub release and verifies its SHA-256 checksum before installing to `~/.local/bin`. It never installs Codex, Claude Code, Cursor, Homebrew, Go, or any other third-party software on your behalf.

A fork can point the installer at its own release repository:

```sh
curl -fsSL https://raw.githubusercontent.com/OWNER/REPOSITORY/main/scripts/install.sh \
  | HUMANSH_REPOSITORY=OWNER/REPOSITORY sh
```

See [security](security.md) for why a same-host checksum establishes integrity but not authenticity.

### From a checkout

```sh
./scripts/install.sh --local
```

The installer replaces the binary atomically and verifies it against the staged build after guided setup. If setup needs attention, the verified binary remains installed so the displayed recovery command works; configuration and shell files remain unchanged. If the binary disappeared while setup was running, the installer atomically restores it from that staged copy. It does not start a second onboarding interaction.

## Choosing shells

Default setup configures the login shell named by `$SHELL`. To choose a shell explicitly:

```sh
./scripts/install.sh --local --shell bash
humansh setup --shell bash          # or afterwards, against the installed binary
```

Setup verifies the selected shell, installs its embedded integration under the XDG data directory, and adds one idempotent managed block to its startup file. Existing installations retain every shell integration already recorded in install state. To discover and configure every compatible Zsh and Bash installation, run:

```sh
humansh setup --advanced
```

### Bash version floor

Bash integration requires **Bash 4.3 or newer**, so humansh can safely capture and restore existing Readline shell-command bindings. macOS still ships Bash 3.2; install a current one with `brew install bash`.

Quick setup reports an actionable error when the selected Bash is too old. The advanced compatibility report lists installed shells, skips an unsupported Bash, and can continue with Zsh. After installing a current Bash, run `humansh setup --shell bash` to select it or `humansh setup --advanced` to add it alongside Zsh.

### Shell modes

Zsh supports Smart Enter, where one key classifies the line. Bash uses explicit translation, because Readline cannot safely make Enter conditionally accept or replace the buffer.

Each managed block exports the resolved binding values before sourcing its immutable, hashed shell asset, so changing a binding never modifies the asset itself.

## The default flow

On a fresh machine, setup silently checks the login shell and installed CLI providers. If exactly one provider is usable, it selects it automatically. If several are usable, it asks one provider question. The flow separates provider choice, review, and the final next step instead of presenting a block of explanatory prose:

```text
humansh setup

Choose your AI provider

  1  Codex       default
  2  Claude Code
  3  Cursor CLI

  AI provider [1]:

Review
  Shell      Zsh
  Provider   Codex
  Startup    Update ~/.zshrc
  Safety     Commands wait for your review

  Continue? [Y/n]:

  ✓ Codex ready

🎉 Humansh is ready!

  Settings   `humansh setup --advanced`

  Try it: open a new terminal, type `list files`, press Enter to translate, then Enter to run.
```

After confirmation, setup runs one minimal provider check, applies the already prepared startup-file plan, and ends with one example showing how to start. A healthy rerun preserves the saved provider, preferences, and installed shell set; it asks no questions and does not spend provider quota on another live check. If an update or repair would change a startup file, setup names the exact file and asks once.

`--yes` accepts the concise plan non-interactively. `NO_COLOR=1` disables styling. Provider checks show an in-place loader on a terminal; redirected output gets one stable `Checking…` line instead.

Setup preserves startup-file symlinks, applies all shell changes transactionally, and refuses to apply a stale reviewed plan. Pressing Ctrl-C at a prompt or during a provider check exits with status 130 and leaves configuration and shell files unchanged.

## Advanced setup

Run `humansh setup --advanced` to use the full six-section editor. It exposes shell compatibility, provider and executable selection, model, directory-context privacy, timeout, Smart Enter, and shortcuts, followed by the exact managed-block patch and final confirmation. At a shortcut prompt, type a readable value such as `Ctrl-G`, `Ctrl-X Ctrl-T`, or `Esc t`.

OpenRouter key/model configuration and pinning a particular Claude or Cursor executable also live in the advanced flow. Existing command flags such as `--provider`, `--shell`, `--repair`, and `--no-shell-change` remain available; combine provider-specific customization with `--advanced` when the quick path directs you there.

## Providers during setup

Quick setup asks about providers only when multiple installed CLI providers are usable. With one candidate it selects that provider automatically. A healthy existing setup silently retains the saved provider. Advanced setup shows the full four-provider menu and uses the saved provider as its default answer.

Provider discovery is non-inference: it checks whether each CLI executable exists without calling optional login, status, version, or help commands. After confirmation, fresh quick setup sends one fixed minimal prompt through a fresh isolated subprocess. That live check may consume a small amount of provider quota.

Authentication belongs to the selected CLI distribution. Humansh neither infers its billing mode nor starts a login flow. This supports centrally managed corporate distributions whose inference command works while login subcommands are intentionally disabled. If a quick-flow probe fails, setup shows the provider's message verbatim after credential/control filtering and length bounding. It does not categorize the wording or derive provider-specific recovery commands. Setup makes no changes and asks the user to fix the provider issue before retrying.

If several Claude or Cursor CLI installations are present in `PATH`, advanced setup lets you keep automatic selection or pin one exact executable. Shell aliases and the Cursor editor launcher are intentionally not used.

A fresh setup or explicit provider change requires **one live, responding provider**. With none selected it stops before writing credentials, configuration, or shell integration. The minimal probe verifies provider reachability; run `humansh provider test NAME` to verify the complete production structured-output invocation and all mandatory safety flags.

### OpenRouter

`humansh setup --advanced --provider openrouter` configures OpenRouter in place; the standalone `humansh provider configure openrouter` remains available for changing the model later. Both flows:

1. Accept the key without echo and validate it through the read-only key-status endpoint.
2. Use read-only model metadata to require `structured_outputs`, not merely basic `response_format` support. Incompatible models are rejected before any model credits are spent, with a link to OpenRouter's filtered compatible-model list.
3. Run one minimal metered request against humansh's exact strict-output schema. This is the only billed step, and it is disclosed before it runs.
4. Stage the result until final confirmation.

Paste the next model ID directly into the repeated model prompt — there is no intervening yes/no question, and `back` returns to the AI-provider menu. An already-valid key is not rechecked for each model attempt.

If the compatibility check fails, setup stops rather than saving an unusable configuration. Changing the model manually with `humansh config set` does **not** mark it proven; use guided setup or `provider configure` for that.

## Uninstalling

```sh
humansh uninstall            # removes integrations, keeps config and credentials
humansh uninstall --purge    # also removes them, after explicit confirmation
```

Declining the purge confirmation cancels the entire uninstall without changing any files. Use `--yes` only for an intentional non-interactive purge. From a checkout, `sh scripts/uninstall.sh [--purge]` and `make uninstall` do the same.

A child process cannot alter its parent shell, so restart a shell only if it already has humansh loaded in memory.
