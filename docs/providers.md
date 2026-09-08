# Providers

The selected provider is explicit in `config.toml`. `humansh` never silently changes providers and never falls back to metered OpenRouter.

Codex, Claude Code, and Cursor own their authentication. This includes vendor installs, centrally managed corporate distributions, and wrappers that intentionally omit login/status commands. Humansh does not call or parse optional login, logout, auth-status, version, or help surfaces, and it does not infer billing mode from undocumented CLI output. A successful call proves that the selected CLI can currently reach its provider; account and billing policy remain the responsibility of that CLI distribution and its administrator or user.

The shared child-environment allowlist excludes variables Humansh does not explicitly need, including generic API-key and endpoint overrides, so unrelated parent-shell secrets cannot silently alter a call. Provider adapters do not maintain or inspect auth-override key tables. Humansh never changes or logs out provider credentials.

## Readiness checks

| Flow | Provider work | Purpose |
|---|---|---|
| `provider list`, `doctor`, setup discovery | Non-inference discovery | Find configured executables without assuming optional CLI subcommands. |
| Fresh setup provider, `provider use` | One constant prompt | Verify the CLI's normal inference route; may consume a small amount of quota. |
| `provider test` | One real structured translation | Verify the complete production argument/output contract. |
| Normal translation | One real structured translation | No separate auth, version, help, or capability preflight. |

The minimal CLI probes are `codex exec <constant-prompt>`, `claude -p <constant-prompt>`, and `cursor-agent -p <constant-prompt> --trust`. Humansh gives Codex a private, empty Git worktree so its probe does not need an optional repository-check flag. Cursor requires workspace trust before contacting the model, so Humansh acknowledges trust only for its own private empty probe directory. Output must equal the fixed `HUMANSH_READY` marker. Each probe has the configured timeout and bounded output; a failure returns the provider's credential-redacted, control-cleaned, length-bounded stdout/stderr detail.

Quick setup asks which provider to use only when multiple installed CLI providers are candidates; it automatically uses a sole candidate. It probes only the selected provider, and only for a fresh setup or an explicit provider change. A healthy rerun preserves the saved provider without a prompt or another probe. Setup never starts a login flow. A failed probe is opaque: Humansh prints the provider-owned message verbatim after credential/control filtering and length bounding, adds one generic fix-and-retry instruction, and exits before saving configuration or shell files. It does not classify the wording or derive provider-specific recovery commands. `humansh setup --advanced` retains the fix-and-retry provider workflow and full four-provider menu.

The minimal check intentionally does not prove support for every production isolation or structured-output option. `humansh provider test NAME` is the full compatibility test. Normal translation uses the same strict invocation and fails closed—with the exact bounded provider error—if a managed distribution rejects a required production option.

## Codex

Codex readiness uses only its non-interactive `exec` surface. It does not inspect a Codex auth record, parse login status, or force a particular login method. Every translation runs in a private empty directory with a minimal environment. The invocation uses an exact argument allowlist, read-only sandboxing, `approval_policy="never"`, ephemeral history, ignored user/project rules, and mandatory `features.shell_tool=false` plus `features.unified_exec=false`. Read-only sandboxing is a backstop; it does not disable shell execution by itself.

The complete strict settings are enforced on every translation. Any rejected flag or config key fails closed; Humansh never retries with a weaker invocation.

Codex has no supported maximum-turn setting in the tested versions. `humansh` parses only the completed `--output-last-message` file and ignores schema-shaped intermediate stdout. A final `ok` object without a command is a neutral, retryable exit-25 incomplete response.

Before qualifying a Codex CLI version for release, run the opt-in behavioral isolation test with a working provider-managed account. It may consume provider quota and is intentionally skipped in ordinary CI:

```sh
HUMANSH_REAL_CODEX_ISOLATION=1 \
HUMANSH_REAL_CODEX_MODEL=MODEL \
go test ./internal/llm/codex -run TestRealCodexBehavioralIsolation -count=1
```

Repeat it for every supported CLI version. The test places a distinctive tripwire in the private working directory, baits the model to inspect it, and verifies that mandatory shell-tool isolation prevents the secret from appearing in the completed response.

Because `--ignore-user-config` bypasses the user's model selection, an unset humansh model uses Codex's built-in default. Copy an explicit model with:

```sh
humansh config set providers.codex.model MODEL
```

## Claude Code

Claude readiness uses exactly the selected executable's normal print mode, `claude -p <constant-prompt>`. A centrally managed distribution may disable `claude auth ...`; that does not matter when print mode works. Humansh does not invoke the disabled command.

Claude's documented `CLAUDE_CODE_OAUTH_TOKEN`, `CLAUDE_CODE_OAUTH_REFRESH_TOKEN`, and `CLAUDE_CODE_OAUTH_SCOPES` variables are narrowly forwarded only to the Claude subprocess; their values are never printed or persisted. Probes and translations also receive `HOME`, absolute Claude/Anthropic/XDG credential-storage roots, and non-secret user identity fields needed to locate the same provider-managed credential store. Unrelated inherited variables remain excluded.

Executable discovery uses configured `providers.claude.binary`, then the first `claude` in `PATH`, and finally the native installer's fixed `~/.local/bin/claude` location. If multiple executables are present, interactive setup can retain automatic selection or pin one absolute path; shell aliases are not treated as executables.

Production translation uses safe mode, `--tools ""` to remove normal built-ins, an explicit `mcp__*` denial, no Chrome, no slash commands, no session persistence, and a three-turn cap so Claude's structured-output workflow can finish. It deliberately does not use a blanket `*` deny because that also removes Claude's synthetic `StructuredOutput` mechanism. Required options are not pre-probed with version/help/auth commands: a rejection is returned directly by the production call.

To pin an installation outside setup, or restore automatic PATH selection:

```sh
humansh config set providers.claude.binary /absolute/path/to/claude
humansh config set providers.claude.binary auto
```

Use `humansh provider test claude` to check the complete structured invocation. If it fails, follow the provider or corporate distribution's own supported authentication procedure; Humansh does not guess one.

## Cursor CLI

Cursor readiness uses `cursor-agent -p <constant-prompt> --trust` in a Humansh-created empty directory and treats authentication as provider-managed. The trust flag acknowledges only that disposable workspace; no user or project files are present. Automatic executable selection prefers `cursor-agent` and falls back to the legacy-compatible `agent` name; it never invokes the `cursor` editor launcher. If separate installations are present, setup can pin one absolute path. To change it later:

```sh
humansh config set providers.cursor.binary /absolute/path/to/cursor-agent
humansh config set providers.cursor.binary auto
```

Known parent-shell API-key, auth-token, endpoint, authless, local, and Bedrock overrides are not inherited by the isolated Cursor child. Verify the complete path with:

```sh
humansh provider test cursor
```

Translation runs with `--print --output-format json --mode ask --sandbox enabled --trust` from a new mode-0700 empty directory. `--trust` applies only to that disposable empty workspace and prevents an interactive workspace-trust prompt. Ask mode is Cursor's documented read-only mode; unlike Codex and Claude, Cursor does not currently expose a native no-tools flag or JSON-schema output flag. Humansh therefore supplies the canonical schema and request together on stdin, asks Cursor not to inspect external state, extracts only the final JSON result envelope, and applies the same strict local JSON and semantic validation used for every provider. The empty workspace prevents project files and project rules from being discovered, but Cursor may still have provider-managed read-only capabilities; humansh does not claim they are removed.

An optional model can be chosen during setup or with:

```sh
humansh config set providers.cursor.model MODEL
```

## OpenRouter

OpenRouter is explicitly metered. Choose it from the default setup provider menu or with `humansh setup --advanced --provider openrouter` to configure it without leaving the wizard. Setup points to `https://openrouter.ai/settings/keys` for creating a key and `https://openrouter.ai/models` for copying a concrete `provider/model` ID, reads the key without echo, and stages the credential and settings until the final review is confirmed. The separate `humansh provider configure openrouter --model provider/model` command remains available for later model changes. Humansh stores a pasted key in macOS Keychain when possible, otherwise in a dedicated mode-0600 credentials file. `OPENROUTER_API_KEY` has highest precedence, skips the key prompt, and is never persisted.

After OpenRouter and a concrete model are explicitly selected, configuration first checks the model's read-only catalog metadata. It requires the explicit `structured_outputs` capability; `response_format` alone is insufficient. Models that fail this free check never receive a completion request. Interactive setup immediately repeats the model-ID prompt so the next `provider/model` value can be pasted directly; it does not put a yes/no prompt between model attempts. Enter `back` to return to the provider menu. The key is validated once and is not rechecked for every candidate model. A model that passes metadata receives one disclosed, automatic, minimal metered request with the real strict structured-output schema. The probe uses one short message and caps output at 128 tokens; only the concrete model slug that passes is saved. `openrouter/auto` is not accepted as a runtime default. Requests omit both `tools` and `tool_choice`; the wire schema omits unsupported `$schema` and `maxLength`, while local validation retains all bounds.

Before the metered probe, humansh calls `GET /api/v1/key` to validate the key and `GET /api/v1/model/{author}/{slug}` to inspect the model without using model credits. Completion requests opt into OpenRouter router metadata; safe provider error text and routing summaries are retained so a 404 caused by endpoint filtering is not mislabeled as a nonexistent model. Persisted `structured_output_proven` and `structured_output_model` fields tie the proof to the exact chosen model; the config command clears both on a model change, a stale manual mismatch is rejected, and runtime translation fails closed until configure succeeds again. The configured base URL is pinned to `https://openrouter.ai/api/v1` so the credential cannot be redirected to a different host.

## Measured cost and latency

Release documentation must record measurements using the table below. Real account-backed measurements have not yet been run in this checkout; do not treat the pre-implementation review values as a performance promise.

The reproducible Codex measurement gate first runs the minimal live probe, then sends five fixed, non-sensitive requests by default through the production provider adapter, structured-response decoding, and local response/command validation. It accepts 3–20 samples. Client-version and token fields remain `unavailable` because Humansh intentionally does not invoke a version command and the isolated final-message interface does not expose usage accounting:

```sh
HUMANSH_REAL_CODEX_MEASUREMENTS=1 \
HUMANSH_REAL_CODEX_MODEL=MODEL \
HUMANSH_REAL_CODEX_SAMPLES=5 \
go test ./internal/llm/codex -run TestRealCodexReleaseMeasurements -count=1 -v
```

| Provider/model | Client version | Samples | Input/prompt tokens | Output tokens | Total tokens | p50 | p95 | Date |
|---|---|---:|---:|---:|---:|---:|---:|---|
| Codex | pending account-backed release test | — | — | — | — | — | — | — |
| Claude | pending account-backed release test | — | — | — | — | — | — | — |
| Cursor | pending account-backed release test | — | — | — | — | — | — | — |
| OpenRouter | pending configured-model release test | — | — | — | — | — | — | — |

The specification review recorded one Codex warning baseline at roughly 7,507 total tokens and several seconds on 0.148/0.149. It was not produced by this release checkout, lacks p50/p95 sampling, and is not a release measurement.
