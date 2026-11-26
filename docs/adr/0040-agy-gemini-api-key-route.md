<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# 0040. Run agy with a Gemini API key through the metered proxy

- **Date:** 2026-11-20
- **Status:** Accepted

## Context

[ADR-0003](0003-cli-auth-without-secrets.md) chose a proxy-held Google OAuth
refresh token for `agy`, because the CLI had no API key or base URL option. It
was never built. [ADR-0009](0009-metered-sandbox-proxy.md) therefore keeps all
Google hosts closed, and no `agy` turn can reach a model today.

A spike on 2026-11-20 checked the pinned `agy` 1.5.9 binary from
`botbox/sandbox:dev`. It ran in an internal Docker network against a local fake
Gemini server, which also logged every other connection attempt. No real model
API was called.

### Spike results

**Authentication.** Since 1.1.13, `agy` can use the Gemini API directly. It
needs all three of these:

- `GEMINI_API_KEY` in the environment
- `modelProvider: "gemini"` in `settings.json` or `--settings`
- optionally `GOOGLE_GEMINI_BASE_URL` for a custom endpoint

With these, `agy` started headless without a Google sign-in. The tests used
`GEMINI_API_KEY=botbox-placeholder` and
`GOOGLE_GEMINI_BASE_URL=http://egress:8080/llm/google`:

- A plain `http://` URL on a non-loopback host was accepted.
- The path prefix was kept.
- Each model call was
  `POST /llm/google/v1beta/models/{model}:streamGenerateContent?alt=sse`. It
  carried the header `X-Goog-Api-Key: botbox-placeholder` and the user agent
  `google-genai-sdk/1.77.0`.
- The first request body was about 38 KB: 14 tool declarations and a system
  instruction of about 16 KB.

**Model names.** `--model` accepts API IDs (`gemini-3.8-flash`) and display
names (`Gemini 3.8 Flash (Low)`). A display name maps to the API ID plus a
`thinkingLevel`. Without `--model`, the request went to
`gemini-3.1-pro-preview`. An unknown model fails the turn and prints the
available models.

**Other egress.** Headless runs tried `CONNECT play.googleapis.com:443`
(telemetry). The interactive start also tried `www.gstatic.com:443`. Blocking
both did not affect the turn. `enableTelemetry` is not a valid `settings.json`
key; `--settings` rejects unknown keys.

**Interactive start.** In API-key mode there is no sign-in screen. The first
start shows a colour scheme picker and a "Terms of Service & Data Use" screen.
On that screen the data collection checkbox is pre-selected.

**Stream input.** 1.5.9 requires `{"event":"user","message":{...}}`. The agent
currently sends `{"type":"user",...}`. agy then fails the turn with
`stream input message is missing the "event" field` and exit code 1.

**Stream output.** The output does not match `decodeAgyLine` or
`testdata/agy_stream.jsonl`. That fixture was not recorded from a real binary.
The observed shape:

```json
{"event":"init","conversation_id":"…","init":{"cwd":"/home/bot","tools":["…"],"permission_mode":"always-proceed"}}
{"event":"step_update","step_update":{"conversation_id":"…","step_index":1,"state":"DONE","step_type":"agent_response","text_delta":"pong\n","usage":{"input_tokens":234,"output_tokens":7,"thinking_tokens":0,"cache_read_tokens":1000,"total_tokens":241}}}
{"event":"step_update","step_update":{"step_index":2,"state":"ACTIVE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"echo spike-ok"}}}}
{"event":"step_update","step_update":{"step_index":2,"state":"DONE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"echo spike-ok"},"output":"…spike-ok\r\n"}}}
{"event":"result","result":{"conversation_id":"…","status":"SUCCESS","response":"done\n","num_turns":1,"usage":{"input_tokens":468,"output_tokens":14,"thinking_tokens":0,"cache_read_tokens":2000,"total_tokens":482}}}
```

Notes on the output:

- **Text.** With a slowly streaming fake, ACTIVE `agent_response` steps carry
  real text deltas. The DONE step carries only the rest. Model thoughts are
  not written to the stream.
- **Tool IDs.** Tool steps have no call ID. `step_index` identifies them.
- **Tool errors.** A failed tool ends with `state: "ERROR"` and
  `tool_info.error.message`. A shell command with a non-zero exit code still
  ends `DONE`.
- **MCP tools.** agy calls every MCP tool through its own `call_mcp_tool`
  function, with `ServerName`, `ToolName` and `Arguments`.
- **Usage.** It appears on each `agent_response` step and again as the total
  in `result`.
  - `input_tokens` excludes cached tokens. The fake reported
    `promptTokenCount: 1234` with `cachedContentTokenCount: 1000`, and agy
    reported `input_tokens: 234`.
  - `output_tokens` includes thinking tokens. With 9 candidate tokens and 40
    thought tokens, it reported 49.

**Permissions.** Without `--dangerously-skip-permissions`, a tool that needs
approval is auto-denied. The tool step ends `DONE` without output. `result`
lists `denied_actions` (`[{"action":"command","display_name":"Bash"}]`), and
stderr says the command was auto-denied. The exit code is 0.

**Errors.** For HTTP 401 and 403, agy fails the turn immediately with exit
code 3, `result.status: "ERROR"` and a stderr line `AGY_ERROR: {json}`. For
HTTP 429, it tried five times over about 40 seconds. That held for the proxy's
`budget_exceeded` body and for a Gemini `RESOURCE_EXHAUSTED` body with a
per-day `QuotaFailure`. If `--print-timeout` expires first, agy exits with
code 0 and partial output. The runner must use `result.status`, not the exit
code.

**Not tested.** Certificate trust and pinning were not tested, because this
route uses plain HTTP to the proxy. Application Default Credentials
(`AGY_ADC_AUTH`) and `GOOGLE_VERTEX_BASE_URL` were not tested either.

### Terms

- The [Antigravity terms](https://antigravity.google/terms), section 6, say
  that accessing the service through third-party software is a breach of the
  agreement. Their example is a third-party tool with Antigravity OAuth. The
  consequence can be account suspension. A
  [forum answer](https://discuss.ai.google.dev/t/can-a-third-party-desktop-app-run-the-official-antigravity-cli-with-the-user-s-own-google-ai-pro-ultra-subscription/190447)
  says the same about running `agy` in a third-party app with a personal
  Pro/Ultra login. It points such apps to a Gemini API key instead. The
  poster's staff status could not be confirmed.
- The [Gemini API terms](https://aistudio.google.com/docs/terms) cover
  developers who make API clients available to users. Only paid services may
  be used for users in the EEA, Switzerland and the UK. Paid services do not
  use prompts or responses to improve Google products. Unpaid services may use
  them.

## Options Considered

### Option A — Gemini API key on the metered `/llm/google` route

- **Pros:** Works like Claude Code and Codex. The sandbox holds only a
  placeholder. No MITM is needed, and no Google host is opened. It is
  documented, and the spike confirmed it with the pinned binary. The terms
  allow it with a paid project.
- **Cons:** Workspaces need a Gemini API key on a billed Google Cloud project.
  Personal Google AI subscriptions cannot be used. The proxy needs a Gemini
  usage meter and prices for Gemini models.

### Option B — Proxy-held Google OAuth token (ADR-0003, option 2)

- **Pros:** Could use the quota of a person's Google AI Pro or Ultra plan.
- **Cons:**
  - Running agy inside bot.Box with Antigravity OAuth is exactly the use that
    section 6 of the terms forbids. The user's Google account can be
    suspended.
  - Injecting the OAuth token on the Gemini API route does not reach the plan
    quota. With a Google sign-in, agy calls the internal Cloud Code API on
    `cloudcode-pa.googleapis.com` (`/v1internal:streamGenerateContent`,
    `loadCodeAssist`, `retrieveUserQuota`). The plan quota is only there.
    A token on `generativelanguage.googleapis.com` bills a Cloud project
    instead.
  - Reaching that quota would need one of two things. The gateway could
    translate requests to the undocumented internal API and refresh tokens
    with Antigravity's own OAuth client. Or it could intercept
    `cloudcode-pa.googleapis.com` and fake agy's token refresh.
  - Claude Code and Codex differ. Anthropic supports `claude setup-token` for
    headless use, and OpenAI documents token sharing (ADR-0030). Google
    offers no such program for Antigravity.
  - Rejected. Reconfirmed on 2026-11-20 when plan support was requested.
    Plan holders use their monthly Gemini API credits through an API key
    instead (see Consequences).

### Option C — Application Default Credentials (Vertex)

- **Pros:** Fits Google Cloud customers with enterprise terms.
- **Cons:** Needs Google Cloud credentials and a Vertex endpoint override. The
  spike did not test this. Possible follow-up.

### Option D — Credentials inside the sandbox (ADR-0003, option 3)

- **Cons:** Breaks the zero-secret sandbox invariant. Option A makes it
  unnecessary. Rejected.

## Decision

Use Option A. It replaces the `agy` part of ADR-0003.

- **Secret.** A workspace or bot secret `llm-google` holds a Gemini API key.
  Its domain must match `generativelanguage.googleapis.com`. The turn runner
  requires it before an agy turn, as for the other CLIs.
- **Route.** The proxy routes `/llm/google/*` to
  `https://generativelanguage.googleapis.com`. It removes the placeholder
  `X-Goog-Api-Key`, drops any `key=` query parameter and sets the real key.
  Budget, pause, guardrail and model price checks run as on the other model
  routes.
- **Metered requests.** Only `POST /v1beta/models/{model}:streamGenerateContent?alt=sse`
  and `POST /v1beta/models/{model}:generateContent` are metered. The model
  comes from the path, never from the body. A request may not ask for more
  than one candidate, because the output guardrail inspects one candidate per
  chunk.
- **Usage.** The meter reads `usageMetadata`. Stream chunks carry running
  totals, so the last report counts. Input is `promptTokenCount` plus
  `toolUsePromptTokenCount`, and it includes `cachedContentTokenCount`.
  Output is `candidatesTokenCount` plus `thoughtsTokenCount`. A stream counts
  as complete only after a finish reason.
- The agent starts agy with `GEMINI_API_KEY=botbox-placeholder`,
  `GOOGLE_GEMINI_BASE_URL=<proxy>/llm/google` and
  `--settings '{"modelProvider":"gemini"}'`. The prompt uses the `event`
  input format.
- The agent passes `--dangerously-skip-permissions`, as for claude and codex.
  Headless agy cannot ask for permission and would otherwise deny every tool.
  Per-bot `permissions.allow` rules do not exist yet.
- CONNECT to `googleapis.com`, including `play.googleapis.com`, stays denied.
- **Guardrails.** The SSE filter reads Gemini text parts
  ([ADR-0035](0035-llm-proxy-guardrails.md)):
  - Thoughts and thought signatures are sent before the checked text, so the
    lookahead window keeps running.
  - Tool calls and the finish reason are sent after it.
  - Frames use the upstream line ending. The SDK in agy rejects a stream that
    mixes `\r\n\r\n` and `\n\n` separators.
  - Request scanning skips `inlineData`, `fileData` and `thoughtSignature`.
    A redaction there would break a screenshot or the next request.
- **Blocked output.** A blocked stream ends with `finishReason: SAFETY`. agy
  still retries a stopped stream up to five times, and each retry is a new
  billed request. So the proxy refuses the next Google request of that bot
  within two minutes with a 403, before any upstream call. agy then ends the
  turn at once. Only one request is refused, so a later turn still works.

## Consequences

- **Agent and engine (done).**
  - The agent sends the `event` input format.
  - It passes `--settings`, `--dangerously-skip-permissions`, the placeholder
    key and the `/llm/google` base URL.
  - The parser follows the 1.5.9 records ([ADR-0004](0004-cli-stream-formats.md)).
  - The `agy_*.jsonl` fixtures are recordings from 1.5.9.
  - `make test-sandbox-agy` runs the pinned binary against a loopback Gemini
    and MCP fixture in CI.
- **Turn runner and proxy (done).** The route, the meter and the guardrail
  changes above are in place. Real agy 1.5.9 runs were checked through the
  proxy and the SSE filter against a fake Gemini server: a clean turn, a
  redaction across chunks, and a block that ends the turn after one upstream
  call.
- **Google AI plans.** Google AI Pro and Ultra include monthly Google Cloud
  credits: 10, 40 or 100 USD. They apply to the AI Studio billing account
  after a 5 USD prepayment, and API keys of its projects draw on them. A plan
  holder therefore uses the plan through an AI Studio key in `llm-google`,
  with no extra code. Only the primary subscriber may use these credits. So
  a person's key belongs at bot scope, on a bot that person runs. The
  included Antigravity quota is not available (Option B).
- **Prices.** Admins price every Gemini model that agy may call. agy's
  subagents can use other models than the bot model, and a budgeted request
  for an unpriced model is refused.
- **Open decision.** An exhausted budget still answers 429. agy retries that
  for about 40 seconds before the turn fails. No upstream call is made.
- **Known gap.** A stream stopped by a guardrail settles with unknown usage,
  as on the other routes. That blocks monetary budgets until an admin
  reconciles it.
- **Desktop.** An `agy` launcher for the bot computer sets the same
  placeholder environment and writes `modelProvider` into
  `~/.gemini/antigravity-cli/settings.json`. The person accepts the agy terms
  and data-use screen themself on first start. The launcher does not accept
  them on their behalf.
- **Model field.** Bot model IDs for Google use Gemini API IDs, such as
  `gemini-3.8-flash`.
- **ADR-0003** becomes superseded for `agy` once this ADR is accepted. The
  OAuth refresh exchange and `allow_sandbox_agy_credentials` are not built.
- **Legal check.** The terms reading above is not legal advice. Have it
  reviewed before Google bots are offered to other users.
