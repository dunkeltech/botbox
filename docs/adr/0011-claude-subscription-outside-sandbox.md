# 0011. Claude subscription authentication outside the sandbox

- **Date:** 2026-11-15
- **Status:** Accepted

## Context

Claude CLI can use a Pro/Max subscription as well as a separately billed API key.
The existing proxy only supported Anthropic API keys, so configuring a Claude bot
and model did not connect a user's subscription. Copying a host login directory
into the bot computer would expose real credentials to model-generated commands.
A subscription's null per-request cost must also be distinguished from missing
billing for a metered request.

## Options

1. Mount a user's Claude credentials into the bot. Rejected because the sandbox
   would receive real provider tokens and potentially refresh credentials.
2. Reimplement the OAuth browser exchange inside bot.Box. This adds a separate
   OAuth client integration and provider-specific refresh lifecycle.
3. Use the official CLI's long-lived setup token, keep it in the encrypted secret
   store, and run the official CLI with only a placeholder inside the bot.

## Decision

Use option 3. Users run `claude setup-token` on their own computer, complete the
provider's login, and enter the generated token into the existing write-only
Secrets form. `make claude-login` provides the pinned official CLI in an ephemeral
trusted login-helper container when no local CLI is installed. That helper is
outside the bot environment, has no host mounts or Docker socket, and is removed
when the login command exits. Its token output belongs only in the user's local
terminal and Secrets form, never the chat or application logs.

The Claude-specific Secrets card prepares a bot-scoped `llm-anthropic-oauth`
secret restricted to `api.anthropic.com` with Bearer injection. Existing scoped
RBAC, encryption, domain/path restrictions and write-only responses apply.
At each scope, CLI resolution prefers the OAuth secret to an API key; explicit bot
scope still overrides inherited workspace scope. A bad or expired OAuth token
never causes fallback to an API key. Generic API mode ignores subscription
secrets completely: only the official Claude CLI may use them.

A trusted RPC flag starts Claude with
`CLAUDE_CODE_OAUTH_TOKEN=botbox-placeholder` and without `ANTHROPIC_API_KEY` or
`ANTHROPIC_AUTH_TOKEN`. No real token crosses the agent RPC. The scoped proxy
injects the real Bearer token, preserves the native Claude OAuth beta protocol,
and meters requests. Authentication modes must match the placeholder presented
by the running CLI; a credential rotation cannot silently switch its billing
mode mid-turn. Native provider tunnels remain blocked.

`usage_records.billing_kind` distinguishes `metered` and `subscription` usage.
Subscription requests retain token counts and null cost, even when a model price
exists. They do not require a model price and do not trigger unknown-cost budget
blocking. Monetary budget exhaustion still blocks new requests under the common
admission policy. Proxy request records replace duplicate CLI aggregates.

## Consequences

- The user must approve their personal provider login outside the bot computer.
  Tokens are renewed by running setup again and updating the write-only secret.
- Provider subscription limits still apply. bot.Box records tokens rather than
  estimating a monetary subscription charge or managing the provider's plan.
- The sandbox image must include the updated agent RPC flag handling.
- The actual pinned Claude CLI is tested in a networkless container against a
  local fake provider, proving that its Sonnet alias and OAuth placeholder work
  without an API-key fallback. Other tests cover proxy injection, billing,
  generic API isolation, RBAC, and credential secrecy. No real LLM is used.
- Development and production Next.js output directories are separate so a build
  cannot corrupt the frontend while the user's development server is running.
- Image contexts exclude host credentials, build outputs and development caches.

## Follow-up

[ADR 0030](0030-codex-desktop-chatgpt-plan.md) adds a workspace opt-in to count
Claude subscription usage toward bot, group, and workspace budgets. It uses the
`model_prices` rates for the model ID sent by the CLI, reserves a conservative
amount for supported requests, and settles against the reported input, output,
and cache tokens. The UI shows totals per bot and labels the estimate as an
internal budget charge. Anthropic continues to enforce its own plan limits.

## References

- [Claude Code authentication](https://code.claude.com/docs/en/authentication)
- `claude setup-token --help` in the pinned official 2.1.323 image
