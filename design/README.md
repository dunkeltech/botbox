# bot.Box designs

High-fidelity designs for bot.Box, styled after [dunkel.tech](https://dunkel.tech). The rules behind them (colour, type, components, avatars, accessibility) are in [`STYLEGUIDE.md`](STYLEGUIDE.md), and the raw values are in [`tokens.json`](tokens.json).

| Folder | Contents |
|---|---|
| [`screens/`](screens/) | PNG exports of every screen at 1440 px width |
| [`source/`](source/) | Source files from the design canvas (`.dc.html` plus `canvas.json`). They are reference markup with exact inline styles and need the canvas runtime to render. |

The interactive canvas (clickable prototype with live avatars) lives in Claude: <https://claude.ai/artifact/5G96Q8BbhbtnvJsH1QvVu8>. It is private until shared from the page's **Share** menu.

## Screens

| Screen | File | Used in |
|---|---|---|
| Fleet dashboard | [`fleet-dashboard.png`](screens/fleet-dashboard.png) | Phase 1, 4, 7 |
| First-run setup wizard | [`setup-wizard.png`](screens/setup-wizard.png) | Phase 1 |
| Sign in, invitation & workspace switcher | [`auth-login-invite.png`](screens/auth-login-invite.png) | Phase 1 |
| Workspace settings: general, runtime & danger zone | [`workspace-settings-general.png`](screens/workspace-settings-general.png) | Phase 1, 5, 7 |
| Workspace settings: members, roles & RBAC | [`workspace-settings.png`](screens/workspace-settings.png) | Phase 1 |
| Workspace settings: budgets & model pricing | [`workspace-settings-budgets.png`](screens/workspace-settings-budgets.png) | Phase 4 |
| Workspace settings: shared workspace secrets | [`workspace-settings-secrets.png`](screens/workspace-settings-secrets.png) | Phase 3 |
| Workspace settings: MCP servers & self-hosted GitLab | [`workspace-settings-integrations.png`](screens/workspace-settings-integrations.png) | #18, ADR-0029 |
| Bot workspace: chat + live computer + action timeline | [`bot-workspace.png`](screens/bot-workspace.png) | Phase 2, 4, 5 |
| Bot workspace: chat memory capture & recall | [`bot-workspace-memory.png`](screens/bot-workspace-memory.png) | ADR-0033 |
| Bot workspace: desktop fullscreen overlay | [`bot-workspace-fullscreen.png`](screens/bot-workspace-fullscreen.png) | Phase 2, 5 |
| Bot workspace: loading skeleton & branded indicator | [`bot-workspace-loading.png`](screens/bot-workspace-loading.png) | Phase 2 |
| Bot settings: general, runtime & danger zone | [`bot-settings-general.png`](screens/bot-settings-general.png) | Phase 1, 2 |
| Bot settings: knowledge base & memory policy | [`bot-settings-knowledge-base.png`](screens/bot-settings-knowledge-base.png) | ADR-0033 |
| Bot settings: budgets & usage | [`bot-settings-budgets.png`](screens/bot-settings-budgets.png) | Phase 4 |
| Bot settings: MCP & GitLab integrations | [`bot-settings-integrations.png`](screens/bot-settings-integrations.png) | ADR-0029 |
| Bot settings: secrets & egress | [`bot-settings-secrets.png`](screens/bot-settings-secrets.png) | Phase 3, #31 |
| New bot | [`new-bot.png`](screens/new-bot.png) | Phase 1, 4, 7 |
| Secrets, egress proxy & bot budgets | [`secrets-egress.png`](screens/secrets-egress.png) | Phase 3, 4 |
| Egress activity domain triage & blocklist | [`secrets-egress-triage.png`](screens/secrets-egress-triage.png) | #31 |
| MCP proxy & command access control | [`mcp-proxy-management.png`](screens/mcp-proxy-management.png) | ADR-0029 |
| In-chat secret requests (CLI & isolated broker) | [`chat-secret-requests.png`](screens/chat-secret-requests.png) | #30 |
| Secret input modal (isolated browser broker) | [`secret-input-modal-browser.png`](screens/secret-input-modal-browser.png) | #30 |
| Secret input modal (CLI / HTTPS adapter) | [`secret-input-modal-cli.png`](screens/secret-input-modal-cli.png) | #30 |
| Team chat: group with humans, mentions & approvals | [`team-group-chat.png`](screens/team-group-chat.png) | Phase 6 |
| Team chat: approvals inbox | [`team-approvals-inbox.png`](screens/team-approvals-inbox.png) | Phase 6 |
| Team chat: direct message | [`team-direct-message.png`](screens/team-direct-message.png) | Phase 4, 6 |
| Team chat: new group (bots & humans) | [`team-new-group.png`](screens/team-new-group.png) | Phase 6 |
| Phone layouts (390 px): Workspace & Team chat | [`phone-layouts.png`](screens/phone-layouts.png) | Phase 8 |
| Robot avatar system | [`avatar-system.png`](screens/avatar-system.png) | Phase 1 |

### Fleet dashboard
Bots as cards with live robot avatars, status, current activity, brain and computer. Includes the workspace switcher pill, a "Spend today" overview tile (€ 18.40 of € 30.00, 61 %, token and subscription CLI counts), activity feed and driver health.

![Fleet dashboard](screens/fleet-dashboard.png)

### First-run setup wizard
Displayed automatically while the server has zero registered users (`setupRequired: true`). Shows the one-time server setup secret prompt (with constant-time SHA-256 verification and rate-limit notice), initial `Owner` account fields, first workspace setup (slug, Docker/Kubernetes default driver, reply language), live server stdout terminal preview, and completion invariants (`/api/setup/*` locks to `404` permanently once finished).

![First-run setup wizard](screens/setup-wizard.png)

### Sign in, invitation & workspace switcher
Active workspace switcher bar with cross-workspace 404 isolation notice, password sign-in with rate-limit notice and session security footer (Argon2id, EdDSA, CSRF), paired side by side with invitation acceptance for a locked invitee email.

![Sign in, invitation and workspace switcher](screens/auth-login-invite.png)

### Workspace settings (general, members, budgets, secrets & integrations)
Reorganized into a two-column workspace settings shell (`256 px` grouped left sub-navigation + main content cards, Stitch project `13598702750338265050`):

- **General, runtime & danger zone** (`screens/50d579be537840b3b5eca818c9416cf1`): workspace profile (`name`, read-only `slug`, `defaultLocale`), default sandbox driver (`docker` / `kubernetes`), `desktop_actions` screenshot retention (`1–30 days`), ownership transfer, and dashed amber **Danger zone** workspace deletion.
- **Members, roles & RBAC** (`screens/e7cd130d743c4b93923be92c7b73216b`): active member roster and role selector, pending invitations with amber `Revoke`, single-use invitation link generator, and the §4.1 RBAC permission matrix.
- **Budgets & model pricing** (`screens/5671deebdace4986b4f745a6004d89c0`): monthly workspace budget cap with 80 % / 100 % threshold markers, bot daily caps, and the `model_prices` rate table + edit form.
- **Workspace secrets** (`screens/073343dcb2c84976be78ed2208434a38`): write-only workspace-scoped secrets inherited by workspace bots alongside the dark `terminal` zero-plaintext & `404` isolation invariant card.
- **Integrations (MCP servers & GitLab)** (`screens/9bcf5e8139344e838174fbf3539a5756`): central workspace MCP server registry (`Sync tools` / `Remove`) and approved self-hosted GitLab HTTPS origins with revocation notice.

![Workspace settings — general, runtime and danger zone](screens/workspace-settings-general.png)

![Workspace settings — members, roles and RBAC](screens/workspace-settings.png)

![Workspace settings — budgets and model pricing](screens/workspace-settings-budgets.png)

![Workspace settings — workspace secrets](screens/workspace-settings-secrets.png)

![Workspace settings — MCP servers and self-hosted GitLab](screens/workspace-settings-integrations.png)

### Bot workspace
Viewport-filling split workspace (`projects/13598702750338265050`, screen `d1911041525e4599b16c7bcdb21737cd`) with a compact bot identity header (`// BOT WORKSPACE`), internally scrolling session chat on the left (`400 px`), and a live XFCE desktop over noVNC on the right with unified toolbar tabs (`Desktop`, `Actions`, `Terminal`, `Files`, `Tool calls`), `Take control` / hand-back bar, **Fullscreen** toggle, and a docked 4-cell resource telemetry strip (`Image`, `CPU`, `Memory`, `Home volume`).

![Bot workspace](screens/bot-workspace.png)

- **Chat memory capture & recall** (`screens/30164c6b88a34a518d93b99d7e5a75fe`): inline `Memory saved` card beneath the bot's confirmation reply (`Always in context` / `On-demand` mode pill, tags, token estimate, `Edit`, amber `Undo`, and `Open in Knowledge base →`) paired with memory recall/store entries in the `Tool calls` pane.

![Bot workspace — chat memory capture and recall](screens/bot-workspace-memory.png)

### Bot desktop fullscreen & loading state
- **Desktop fullscreen overlay** (`projects/13598702750338265050`, screen `ce23159708034299b16c7bcdb21737cd`): full-viewport `terminal` (`#14201a`) stage maximizing the 16:10 XFCE viewport beneath a pinned `dark-2` (`#1f2d1f`) control bar (`Take control` / `Hand back`, toggleable slide-over `Chat` drawer, browser fullscreen toggle, and `Exit fullscreen` with `Esc`).
- **Workspace loading state** (`projects/13598702750338265050`, screen `66cac315501a4b92b6f81b6dc25f9109`): zero-layout-shift workspace skeleton matching the split layout with a centered `bot.Box` hexagon-robot loading indicator inside the `terminal` viewport.

![Bot desktop fullscreen overlay](screens/bot-workspace-fullscreen.png)

![Bot workspace loading state](screens/bot-workspace-loading.png)

### Bot settings (general, budgets, integrations & secrets)
Reorganized into a two-column bot settings shell matching Workspace settings (`256 px` sticky left sub-navigation + dark `terminal` isolation card + `14 px`-radius content cards, Stitch project `13598702750338265050`):

- **General & runtime** (`screens/3099ef0d6ba34e8eb368d453c8d1112c`): runtime pause/resume control, identity & global role, deterministic avatar seed + mood preview, model brain & sandbox driver, and dashed amber **Danger zone** deletion.
- **Knowledge base** (`screens/9a8443023c674f1ba27fcf0e75c5cdd1`): `Auto-save memories from chat` toggle, `Always-in-context` 4,000-token budget meter with 80 % marker, `+ Add memory` dialog, search/mode/source/tag filters, inline `Always` / `On-demand` switch, zero-secret memory guard callout, and dashed amber **Danger zone** clear control.
- **Budgets & usage** (`screens/b27c594b488d455b831d378383a6c8c4`, previously `8102d2afcc7241098e928036ef5e01b3` / `70ca340e5c754e348624d46ad3c35bd2`): 5-hour rolling, daily, weekly, and monthly spend caps per bot in a 2×2 meter grid (plus workspace monthly cap context), 80 % amber warning and `429 BudgetExceeded` hard-stop banner, 4-column limit form (blank clears a cap), and per-model subscription/token ledger across all four windows.
- **MCP & GitLab** (`screens/23dcd676db654655a2d4e7bd22b94a39`): workspace-registered GitLab origins with instant revocation warning, per-bot MCP server switches, three-way command policies (`Allow` / `Ask approval` / `Block`), and dark `terminal` MCP audit log.
- **Secrets & egress** (`screens/921383c3acd7489c99d629e2068dd8a1`): CLI subscription setup, write-only proxy secrets, `Allowlist` / `Open internet` policy, allowed & dashed-amber blocked hosts, and dark `terminal` egress triage log.

![Bot settings — general and runtime](screens/bot-settings-general.png)

![Bot settings — knowledge base](screens/bot-settings-knowledge-base.png)

![Bot settings — budgets and usage](screens/bot-settings-budgets.png)

![Bot settings — MCP and GitLab](screens/bot-settings-integrations.png)

![Bot settings — secrets and egress](screens/bot-settings-secrets.png)

### New bot
Sections: Identity with a seeded avatar and reroll, Brain (agent CLI in the sandbox or a model API), Computer (Docker or Kubernetes, image, resources, persistent home), and Secrets & network. A live preview shows what gets created.

![New bot](screens/new-bot.png)

### Secrets, egress proxy & bot budgets
How a secret leaves the sandbox, write-only secrets, the outbound allowlist, a filterable egress log, and per-bot daily/monthly budget meters (with 80 % amber warning, 100 % `429 BudgetExceeded` kill-switch state, and budget raise requests) alongside `usage_records`.

![Secrets and egress proxy](screens/secrets-egress.png)

### Egress activity domain triage & blocklist
Extends the bot **Secrets & network** view (Stitch project `13598702750338265050`, screen `9134a93a74a544c48764366fbaac88ff`) with an explicit **Blocked hosts** list (dashed `amber-line` / `amber-tint` chips overriding both `Allowlist` and `Open internet` modes), one-click **Allow** / **Block** actions directly inside the dark **Recent egress activity** log table, read-only `Via secret` indicators for secret-bound rows, wildcard-block **Edit rule** jumps, applied vs. `202` pending enforcement badges, and pattern-specific **Undo**.

![Egress activity domain triage and blocklist](screens/secrets-egress-triage.png)

### MCP proxy & command access control
Central workspace MCP server registry (`Owner` / `Admin`) paired with per-bot server assignment, three-tier per-command policies (`Allow`, `Ask approval`, `Block` with fail-closed default), sandbox/API runner wiring summary, and dark terminal `Recent MCP proxy activity` log (`projects/13598702750338265050`, screen `5dbbd2440e4f46d6b33b9818c9cb7bfe`).

![MCP proxy and command access control](screens/mcp-proxy-management.png)

### In-chat secret requests & write-only modals
Inline `secret_request` cards inside bot DMs and group chats paired with write-only secret input dialogs for reviewed CLI HTTPS adapters (`github_api`, `gitlab_api`, `gitlab_git`, `npm_registry`, `pypi_upload`, `pypi_simple`) and the isolated browser authentication broker target design (`browser_login` remains gated behind `unsupported_capability` until the isolated broker lands).

![In-chat secret requests](screens/chat-secret-requests.png)

![Secret input modal, isolated browser broker](screens/secret-input-modal-browser.png)

![Secret input modal, CLI HTTPS adapter](screens/secret-input-modal-cli.png)

### Team chat
Bots and humans (`Owner`, `Reviewer`, `Viewer`) appear as colleagues in a Slack-style sidebar next to direct messages, `@` mention autocomplete, group chats with inline approval cards (`new_host`), bot-to-bot turn limits, the Approvals inbox (`Pending`, `Approved`, `Denied`) and the new-group dialog for both bots and humans.

![Team chat, group](screens/team-group-chat.png)

![Team chat, approvals inbox](screens/team-approvals-inbox.png)

![Team chat, direct message](screens/team-direct-message.png)

![Team chat, new group dialog](screens/team-new-group.png)

### Phone layouts (390 px)
Mobile layouts at 390 px width for Bot Workspace (compact header, Desktop/Actions/Terminal/Files tabs, takeover banner, 16:10 XFCE viewport with 44 × 44 px `Tap / Pan` touch controls, 2×2 resource/budget grid and sticky composer) and Team Chat (top bar, open `dark-2` slide-over drawer over scrim with channels, Approvals badge, bots and humans, plus sticky composer).

![Phone layouts](screens/phone-layouts.png)

### Robot avatars
A deterministic robot per seed, with moods that follow the bot's status.

![Avatar system](screens/avatar-system.png)
