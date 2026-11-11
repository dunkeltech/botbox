# bot.Box UI styleguide

bot.Box is a dunkel.Tech project, and its interface inherits the dunkel.tech look: dark forest greens, mint, the blue hexagon trio from the logo, Lato, 14 px corners and the `//` eyebrow. This guide is the source of truth for the frontend. Machine-readable values live in [`tokens.json`](tokens.json); the screens are in [`README.md`](README.md).

## 1. Principles

1. **Bots are colleagues.** Every bot has a name, a face (its robot avatar) and a visible state. Show what a bot is doing right now, not just what it is configured to do.
2. **Security is visible.** Secrets, the egress proxy, allowlists and approvals appear in the UI where they act (for example, a "Token added by the egress proxy" note under a tool call). Never hide them in settings only.
3. **The human stays in charge.** "Take control", "Pause", turn limits and approvals are always one click away.
4. **Calm, technical, precise.** Light surfaces, one accent family, monospace for anything a machine reads (commands, hosts, models, image names, seeds).

## 2. Brand

- **Wordmark:** `bot` + green `.` + `Box`, Lato 900. The dot is `green` (`#3f9147`) on light surfaces and `green-light` (`#5eab60`) on dark ones. This mirrors `dunkel.Tech`.
- **Endorsement:** "a dunkel.Tech project" in the footer.
- **Logo mark:** the dunkel.tech hexagon turned into a robot face: hexagon outline, an antenna with a green tip, two green eyes and a mouth of three blue hexagons. Use it at 30–38 px in headers and 96 px on large surfaces.

```svg
<svg viewBox="0 0 160 160" aria-hidden="true">
  <line x1="80" y1="20" x2="80" y2="10" stroke="#1a2a1a" stroke-width="6" stroke-linecap="round"/>
  <circle cx="80" cy="8" r="7" fill="#49b24d"/>
  <path d="M49 20.3 111 20.3 142 74 111 127.7 49 127.7 18 74Z" fill="none" stroke="#1a2a1a" stroke-width="6" stroke-linejoin="round"/>
  <circle cx="60" cy="64" r="12" fill="#49b24d"/>
  <circle cx="100" cy="64" r="12" fill="#49b24d"/>
  <polygon points="62,91 68.1,94.5 68.1,101.5 62,105 55.9,101.5 55.9,94.5" fill="#8aa8db"/>
  <polygon points="80,91 86.1,94.5 86.1,101.5 80,105 73.9,101.5 73.9,94.5" fill="#6d8fce"/>
  <polygon points="98,91 104.1,94.5 104.1,101.5 98,105 91.9,101.5 91.9,94.5" fill="#3f5f9c"/>
</svg>
```

On dark surfaces, the outline and antenna use `#c9d8c9` and the eyes `#5eab60`.

## 3. Colour

### Core palette

| Token | Hex | Use |
|---|---|---|
| `ink` | `#182019` | Primary text, outlines of avatars |
| `ink-2` | `#3a4a3c` | Secondary body text, lead paragraphs |
| `muted` | `#5d6b60` | Meta text, labels, timestamps (5.6:1 on white) |
| `paper` | `#ffffff` | Cards, inputs, main chat pane |
| `paper-2` | `#f5f8f4` | App background, table headers, quiet panels |
| `line` | `#e3ebe2` | Card borders, dividers, secondary button borders |
| `line-strong` | `#c9d8c9` | Input borders, dashed placeholders |
| `green` | `#3f9147` | Accent fills: dots, meters, switches, icons. **Not for small text** (3.9:1) |
| `green-600` | `#347a3b` | Primary buttons, links, green text (5.3:1 on white) |
| `green-light` | `#5eab60` | Accent on dark surfaces, unread badges, CTA on dark |
| `mint` | `#d6fcd0` | Highlights, success fills, avatar tiles, diff lines |
| `mint-soft` | `#eef8ec` | Selected cards, success chips, system messages |
| `mint-line` | `#cfe0cf` | Borders of mint surfaces |
| `dark` | `#2a3c2a` | Footer, selected segmented control, user chat bubble |
| `dark-2` | `#1f2d1f` | Team chat sidebar |
| `terminal` | `#14201a` | Terminals, code, log tables, top bar of the team chat |

### Blues (from the logo)

| Token | Hex | Use |
|---|---|---|
| `blue` | `#4a5e92` | "Thinking" state, mentions, secondary data (6.3:1 on white) |
| `blue-tint` | `#edf0f6` | Thinking chip, mention background |
| `blue-line` | `#d3d9e8` | Border of blue chips |
| `hex-1` / `hex-2` / `hex-3` | `#8aa8db` / `#6d8fce` / `#3f5f9c` | Logo, avatar palettes, path colour in terminals |

### Warning

bot.Box has no red. Blocked or failed things use amber, so they never rely on red versus green alone.

| Token | Hex | Use |
|---|---|---|
| `amber` | `#b45309` | Blocked egress, failed tests, warnings (5.0:1 on white) |
| `amber-tint` | `#fdf3e7` | Background of warning rows and cards |
| `amber-line` | `#e8b98a` | Dashed border of blocked items |
| `amber-on-dark` | `#f0b46a` | Failures inside terminals, "blocked 403" badge |

### Text on dark surfaces

| Token | Hex | Use |
|---|---|---|
| `on-dark` | `#c9d8c9` | Body text on `dark`, `dark-2` and `terminal` |
| `on-dark-muted` | `#a8bda8` | Meta text on dark (5.9:1 on `dark`) |
| `on-dark-faint` | `#8ea68e` | Comments in code and decorative text only (fails 4.5:1 on `dark`) |

### Semantic: bot status

| Status | Chip background | Chip text | Chip border | Dot | Avatar mood |
|---|---|---|---|---|---|
| Working | `mint-soft` | `green-600` | `mint-line` | `green` | `action` |
| Thinking | `blue-tint` | `blue` | `blue-line` | `blue` | `thinking` |
| Idle | `paper-2` | `muted` | `line` | `#8ea68e` | `idle` |
| Stopped | `paper` | `muted` | `line` | `line-strong` | `offline` |

The status label is always written out next to the dot. Colour is never the only signal.

### Semantic: budget thresholds

The fleet shows known daily and weekly spend (provider spend plus internal subscription charges when enabled) alongside the optional workspace monthly cap. Budget meters follow the Stitch project `13598702750338265050`, screen `382c2e3cc2854be796ffa8ff42c03345` and bot budget screen `b27c594b488d455b831d378383a6c8c4`: show the percentage and 80%/100% thresholds, use amber above the warning threshold, and a dashed border for exhaustion. Meters expose their value through an accessible progressbar role. Bot settings expose 5-hour rolling, daily, weekly, and monthly USD limits to workspace owners and admins (with blank fields clearing a window cap). Warning and exhausted states use text labels with the existing amber tokens. Money amounts never replace unknown costs with a fabricated estimate.

| Threshold | Track fill | Banner / badge bg | Text | Border | Meaning |
|---|---|---|---|---|---|
| 0–79 % (`ok`) | `green` | `mint-soft` | `green-600` | `mint-line` | Normal spend within the configured budget window |
| 80–99 % (`warning`) | `amber` | `amber-tint` | `amber` | `amber-line` | Warning threshold reached; alert surfaced |
| 100 % (`exceeded`) | `amber` | `amber-tint` | `amber` | Dashed `amber-line` | Hard stop (`429 BudgetExceeded`); `amber-on-dark` in terminal logs |

## 4. Typography

- **Sans:** Lato 400, 400 italic, 700, 900 (load via `next/font/google`). Fallback: `system-ui, -apple-system, "Segoe UI", Roboto, Helvetica, Arial, sans-serif`.
- **Mono:** `ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, "Liberation Mono", monospace`. Use it for commands, hostnames, model IDs, image names, seeds, paths and log tables.
- Headings use weight 900, letter-spacing `-0.01em` and line-height 1.1–1.2.

| Style | Size / line-height | Weight | Use |
|---|---|---|---|
| Display | 52 / 1.1 | 900 | Large marketing or spec surfaces only |
| Page title | 44 / 1.15 | 900 | Top of Fleet and New bot pages |
| Entity title | 30–36 / 1.15 | 900 | Bot name in workspace and settings |
| Section | 22 / 1.3 | 900 | Card titles, form sections |
| Subsection | 18–20 / 1.3 | 900 | Aside cards, chat header |
| Body | 16 / 1.6 | 400 | Default text |
| Chat body | 15 / 1.55 | 400 | Messages, sidebar items |
| Small | 14 / 1.5 | 400–700 | Helper text, meta |
| Caption | 13 / 1.5 | 400–700 | Chips, timestamps, mono values |
| Overline | 12–13, uppercase, tracking `.04–.08em` | 700 | Stat labels, sidebar section headers |

**Eyebrow** (from dunkel.tech): 13 px, uppercase, tracking `.06em`, weight 700, `muted`, preceded by `// ` in `green` at weight 900. Use it above page titles (`// Fleet`, `// New bot`).

## 5. Space, size, layout

- **Base unit:** 4 px. Common steps: 4, 6, 8, 10, 12, 14, 16, 18, 20, 22, 24, 28, 32, 40, 56, 64.
- **Page container:** `max-width: 1360px`, side padding 24 px (16 px on phones).
- **Card padding:** 18–24 px. Gap between cards: 16–22 px. Gap between page sections: 22–28 px.
- **Touch targets:** at least 44 × 44 px for anything clickable. Icon buttons are 40–44 px squares.
- **Responsive pattern:** use flex-wrap rows. Main content takes `flex: 999 1 560px; min-width: 0` and the aside `flex: 1 1 300px`, so the aside drops below on narrow screens. Grids use `repeat(auto-fill, minmax(290px, 1fr))`. Wide tables scroll inside an `overflow-x: auto` box.

| Shell | Measurements |
|---|---|
| App header | Full-width `terminal` (`#14201a`) top bar (~54 px tall) across all authenticated views; current nav item white with a 2 px `green-light` (`#5eab60`) underline; workspace switcher pill `<workspace> · <role>`, approvals bell and account menu on the right |
| Workspace settings | Two-column layout under the standard header with `256 px` sticky left sub-navigation (`Workspace`, `Spend & billing`, `Security & integrations` including `/settings/guardrails` for `LLM guardrails`) and `14 px`-radius main content cards. Navigation links wrap horizontally on narrow viewports. The active link uses `mint-soft` fill, `green-600` bold text, and a `3 px` `green` left accent. The owner-only Danger zone uses `amber-tint` with a dashed `amber-line` border. |
| Bot settings | Two-column settings shell (`BotSettingsShell`) with `256 px` sticky left sub-navigation (`Bot`, `Spend & usage`, `Security & integrations`) across `/bots/:id/settings` (`General & runtime`), `/bots/:id/settings/knowledge` (`Knowledge base`), `/bots/:id/settings/budgets` (`Budgets & usage`), `/bots/:id/settings/integrations` (`MCP & GitLab`), and `/bots/:id/secrets` (`Secrets & egress`) |
| Team chat | Same full-width `terminal` top bar with integrated search input, sidebar 264 px (`dark-2`), main pane flexible, right `paper-2` inspector aside 320–340 px (`Details` \| `Tool calls` segmented switch; slide-over sheet on narrow viewports) |
| Bot workspace | Compact identity header (`// BOT WORKSPACE`), `400 px` internally scrolling chat column, flexible computer column with 16:10 desktop viewport, docked 4-cell telemetry bar, and `Fullscreen` overlay mode (`terminal` backdrop, `dark-2` pinned control bar, slide-over chat drawer, `Esc` to exit) |
| Phone shell (390 px) | 16 px side gutters, ≥ 44 × 44 px header and noVNC touch controls (`Tap / Pan`), sticky bottom composer, and a slide-over `dark-2` team sidebar drawer with `overlay` scrim |

## 6. Shape and elevation

| Token | Value | Use |
|---|---|---|
| `radius` | 14 px | Cards, dialogs, large panels |
| `radius-md` | 12 px | Inner panels, segmented-control tracks, composer |
| `radius-sm` | 9 px | Buttons, inputs, icon buttons, small cards |
| `radius-xs` | 5–8 px | Code chips, nav items, badges |
| `radius-pill` | 999 px | Status chips, mood chips, unread counters |
| `shadow` | `0 1px 2px rgba(26,44,26,.06), 0 8px 24px rgba(26,44,26,.07)` | Resting cards |
| `shadow-hover` | `0 6px 16px rgba(26,44,26,.10), 0 18px 40px rgba(26,44,26,.12)` | Hovered cards, dialogs |
| `overlay` | `rgba(20,25,20,.45)` | Dialog backdrop |

## 7. Components

### Buttons
- **Primary:** `green-600` background, white text, weight 700, 44–46 px tall, radius 9, padding 0 18–20 px. Hover: `dark`. An optional trailing arrow `→` moves 3 px right on hover (transition 0.18 s).
- **Secondary:** white background, 1.5 px `line` border, `green-600` text. Hover: `mint-soft` background and `green-light` border.
- **On dark surfaces:** `green-light` background with `terminal` text (6:1), or transparent with a `rgba(255,255,255,.25)` border.
- **Icon button:** 40–44 px square, radius 9, needs an `aria-label`.
- **Disabled:** `#a8bda8` background, `cursor: not-allowed`, keep the label.

### Form controls
- **Inputs, selects, textareas:** 44–46 px tall, radius 9, 1.5 px `line-strong` border, white background. Read-only fields use `paper-2` with a `line` border. Technical values are set in mono.
- The label sits above the field: 14–15 px, weight 700. Helper text goes below in 14 px `muted`.
- **Segmented control and tabs:** a white or `paper-2` track (radius 12, padding 4). The selected segment is `dark` with white text; the others are transparent with `ink-2` text. Use `aria-pressed`.
- **Radio card:** a button with a 2 px border. Selected: `green` border, `mint-soft` background and a filled `green` dot. Unselected: `line` border, white background, an empty dot with a `#a8bda8` ring.
- **Switch:** a `role="switch"` button, 48–52 × 28–30 px. On: `green` track, knob right. Off: `#a8bda8` track, knob left.
- **Checkbox tiles** (group member picker): a 22 px box. Checked: `green-600` fill with a white tick.

### Data display
- **Card:** white, 1 px `line` border, radius 14, `shadow`. On hover, interactive cards lift 4 px and switch to `shadow-hover` with a `green-light` border.
- **Stat tile:** an overline label, the value in 32 px 900 with the unit in 18 px `muted`, then a caption. Meters are 6–8 px tall rounded bars on a `line` track.
- **Status chip:** a pill with an 8 px dot and the label at 13 px weight 700 (see section 3).
- **BOT badge:** 11 px, weight 900, tracking `.06em`, `mint-soft` background, `green-600` text, radius 5.
- **Terminal block:** `terminal` background, `on-dark` text, mono 13 px, radius 9. Prompt user `green-light`, path `hex-1`, failures `amber-on-dark`, cursor block `mint`.
- **Tool-calls sidebar & turn chip:** Tool invocations and outcomes stay out of the chat timeline so conversations remain readable. Each bot reply that used tools shows one compact turn chip (`4 tool steps · 1.9 s →`) that opens and focuses the `Tool calls` sidebar tab (Team Chat) or `Tool calls` computer tab (Bot Workspace). Inside the trace, each invocation + result pairs into one card (`Running` in `blue-tint`/`blue`, `Done` in `mint-soft`/`green-600`, `Failed` in dashed `amber-line`/`amber-tint`, never red) using short sanitized sentences ("Opening https://dunkel.tech", "Opened the page", "Browser screenshot") and optional screenshot thumbnails; raw tool JSON arguments and sandbox paths never appear.
- **System message** (team chat): a `mint-soft` banner with a `mint-line` border, radius 9, 13 px `dark` text and a bot-to-bot (two arrows) icon.
- **Attachment card** (merge request, file, approval): a white card with `shadow`, an icon tile in `mint-soft`, the title in 900, mono meta and a status chip.
- **Budget meter:** 6–8 px bar on a `line` track with tick markers at 80 % (warn) and 100 % (stop). Switches from `green` to `amber` at 80 % and pairs with a dashed `amber-line` / `amber-tint` kill-switch notice at 100 %.
- **Action timeline row (`desktop_actions`):** grouped timeline row inside the Bot Workspace Actions tab showing action verb, viewport coordinates (`1280×800`), 7-day screenshot retention policy ("Screenshots kept 7 days · excluded from logs"), and an amber `409` conflict pill when computer-use is rejected during human takeover.
- **Tables:** header row in `paper-2` with overline labels, rows separated by 1 px `line`. On dark log tables, rows are separated by `dark`.
  > When prototyping in the design canvas, build tables as CSS grids with ARIA table roles, because a loop inside `<table>` breaks the template parser. In the React app, real `<table>` elements are fine.
- **Unread counter:** a 20 px pill, `green-light` background, `terminal` text, weight 900.

### Chat

- Progress updates use the bot's usual avatar, name, and time with a `Progress` or `Blocker` label. Status checks use a separate `Status check` label and show the colleague's state in text. Show the start time for current work and the timestamp of the latest update when available. These entries stay visible in the transcript after reload.
- The workspace and Team DM panels share private history, streamed text, queue positions and explicit desktop-visibility disclosure (ADR-0008). A soft-denied tool must appear as an explicit error notice even when the CLI exits successfully. Budget admission errors use the existing warning treatment and translated copy; never show raw dependency errors.
- **Message row (Slack style):** a 36 px avatar, then the name (weight 900), a BOT badge for bots (or role tag for human teammates with initials avatar and presence dot), and the time in 12 px `muted`. The body is 15/1.55. Consecutive messages from the same sender may later be grouped.
- **Markdown in messages:** Human, bot and streamed chat messages render CommonMark and GFM (headings, lists, blockquotes, scrollable tables, inline code, `terminal` code blocks with a 44 px copy button, and `http`/`https`/`mailto` links). Raw HTML is never interpreted, and Markdown images render as links instead of loading external `<img>` tags.
- **Mentions & `@` autocomplete:** `blue` text on `blue-tint`, weight 700, radius 4, padding 0 3 px. Typing `@` opens a keyboard-navigable `role="listbox"` popover directly above the composer (in flow, without occluding messages) listing bots and humans with presence and role metadata.
- **Group broadcast:** the mention picker also offers `@all`, labelled "Everyone in this group", using the existing mention treatment and 44 px option targets. It addresses group members when a human posts it. Longer words and email addresses do not become broadcasts.
- **Group input request:** show collection progress directly below the bot's question, in a compact panel with the existing `line` border and small radius. Give the panel a polite status role and use text to show each recipient as Waiting, Replied, or No reply received.
- **Delete group:** non-viewer group members see a secondary `Delete group` button in the chat header, including on mobile. It opens the shared native dialog with the group name and explains that deletion affects everyone, removes messages and queued tasks, and retains bots, computers, and cost records. Focus starts on Cancel. Confirmation and Cancel are disabled while deleting; errors stay in the dialog with safe translated text and allow retry. Active bot work or credentials block deletion. Success removes the group and opens the next available group, or the empty state. Use existing button, dialog, warning, focus, colour, and radius tokens.
- **Bot agent:** use the standard select for Claude Code · Anthropic, Codex · OpenAI, agy · Google Antigravity, and Pi · OpenRouter. Keep the model identifier as a labeled text input. Selecting Pi shows the OpenRouter identifier format and the workspace secret name `llm-openrouter` below the fields.
- **Global bot role:** the bot builder and settings expose "Global role & responsibilities" as a multiline free-text field, with helper text explaining that it applies in every group and direct message from the next turn. Use the existing textarea and disabled treatment for viewers; roles have no preset categories. In team chat direct-message details, render roles as safe CommonMark/GFM using the message renderer. Bound the collapsed preview to about four body lines (15 px / 1.55); show a "More" / "Less" text button only when the rendered content overflows. Use `green-600`, a 44 px touch target, visible focus, and `aria-expanded` / `aria-controls`. Recheck overflow when the inspector resizes, reset the preview when the bot or role changes, and reveal any Markdown control that receives keyboard focus.
- **Maximum turn duration:** the bot builder and General & runtime settings use the shared numeric input with a visible minutes label. Helper text states the range of 1 to 120 minutes and explains when a change applies. Users without bot management rights see the disabled field.
- **Approval request card:** inline `amber-tint` card with dashed `amber-line` border in group chat (`new_host`, `raise_budget`, `push_protected`) plus Pending / Approved / Denied filter tabs in the dedicated Approvals inbox (`Approvals 3` counter in sidebar).
- **Workspace chat bubble from the user:** `dark` background, white text, radius `14 14 4 14`, right-aligned.
- **Typing or working indicator:** a 22–28 px avatar in mood `thinking` plus a `blue` 13–14 px weight-700 line, for example "Ferrit is thinking…".
- **Concurrent bot activity:** show one labelled indicator for every active bot above the group composer. Running turns show Thinking before any tool call; tool execution uses the existing working strip. Queued turns and secret waits use explicit text. Prefer a bot's running turn over queued follow-ups. The activity area scrolls at 160 px so the composer stays reachable. Sidebar, member list, and details use the same refreshed bot snapshot. A finished lead does not change colleagues' indicators. Use existing blue and paper tokens; no new status colour or radius.
- **Group turn failures:** show an amber warning above the composer, with the affected bot's name and translated recovery copy. Show only the latest failure per bot; later queued, running, or completed work replaces it. Never show raw provider output or internal error details. Keep the warning area scrollable at 160 px so the composer remains reachable.


- **Composer:** 1.5 px `line-strong` border, radius 12. Toolbar: mention (@), attach and a hint text, with the send button on the right (`green-600`, 44 × 40). Keep typing and mention navigation responsive with long transcripts by keeping draft state local to the composer.

### Navigation
- **App header nav:** full-width `terminal` (`#14201a`) top bar; items are 44 px tall, weight 700, `on-dark`. The current item is white (`#ffffff`) with a 2 px `green-light` (`#5eab60`) inset underline.
- **Workspace switcher pill:** header pill showing `<workspace> · <role>` (e.g. `dunkel.Tech · Owner`), switching workspaces with strict `workspace_id` isolation (cross-workspace resource IDs return `404`).
- **First-run setup wizard:** displayed when zero users exist, pairing a 3-step main form (`01 · Server setup secret`, `02 · Initial Owner account`, `03 · First workspace`) with an `Uninitialized instance` amber status pill, amber rate-limit callout, and a right-hand `terminal` stdout reference block + completion invariants list.
- **Team sidebar:** `dark-2` background (`264 px` on desktop; slide-over drawer over an `overlay` scrim at `390 px`). Section headers are overlines in `on-dark-muted`. Items are 44–48 px tall with radius 8. The active item has a `green-600` background and white text. Unread items are white at weight 900 with a counter. Bot items show a 30 px avatar with a presence dot (10 px, 2 px ring in the sidebar colour) and the current activity in 12 px; human items show initials badges, role labels (`Owner`, `Reviewer`, `Viewer`), and online/away state.
- **Dialog:** a white panel, max-width 620–640 px, radius 14, `shadow-hover`, over the `overlay` backdrop. It has an eyebrow and a title, a close icon button top right, and actions bottom right (secondary first, primary last).

## 8. Robot avatars

Every bot gets a deterministic robot generated from its `avatar_seed`. There are no human faces.

### Algorithm
1. `h = fnv1a32(seed)`: offset basis `2166136261`, prime `16777619`, unsigned 32-bit. The JavaScript reference iterates over UTF-16 code units (`charCodeAt`). **Seeds are restricted to `[a-z0-9-]`**, so byte-wise and code-unit-wise hashing agree and a Go implementation produces the same robot.
2. Parts:
   - `head = h % 4`: rounded rect, hexagon, dome, octagon
   - `sensors = (h >>> 4) % 4`: dual eyes, visor, cyclops, LED clusters
   - `antenna = (h >>> 8) % 3`: ball, double, gear
   - `mouth = (h >>> 12) % 3`: grille, dots, slot
   - `palette = (h >>> 16) % 6`
3. Default seeds are `<slug>-<4 hex>`, for example `atlas-005a`. "Reroll" picks a new suffix.

### Palettes

| Name | Body | Shade | Panel | Sensor | Mouth / marks | Accent | Tile |
|---|---|---|---|---|---|---|---|
| forest | `#3f9147` | `#347a3b` | `#182019` | `#d6fcd0` | `#182019` | `#3f9147` | `#eef8ec` |
| night | `#2a3c2a` | `#1f2d1f` | `#14201a` | `#5eab60` | `#5eab60` | `#3f9147` | `#d6fcd0` |
| steel | `#c9d8c9` | `#a8bda8` | `#1f2d1f` | `#5eab60` | `#1f2d1f` | `#3f9147` | `#f5f8f4` |
| cobalt | `#4a5e92` | `#3f5f9c` | `#14201a` | `#8aa8db` | `#14201a` | `#4a5e92` | `#e4e9f4` |
| sky | `#8aa8db` | `#6d8fce` | `#1f2d1f` | `#d6fcd0` | `#1f2d1f` | `#4a5e92` | `#edf0f6` |
| mint | `#d6fcd0` | `#b5e4ae` | `#2a3c2a` | `#49b24d` | `#2a3c2a` | `#3f9147` | `#e3ebe2` |

All outlines are `ink` at 3 units (viewBox 120). The tile is a rounded square (`rx = 26/120`) and can be switched off (`tile={false}`) for small inline use.

### Moods (driven by bot status)

| Mood | Sensors | Antenna | Extra |
|---|---|---|---|
| `idle` | Opacity 1 → .55 → 1 over 3.2 s | Gear 9 s per turn, glow 3.2 s | none |
| `thinking` | 1 → .4 → 1 over 1.4 s, visor scan 1.4 s | Gear 2.2 s per turn | Thought bubble with three pulsing dots |
| `action` | Blink `1;1;.25;1;1` over 1 s, visor scan .8 s | Gear .9 s per turn, glow .6 s | Two signal rings expanding from the antenna tip (1.4 s, staggered) |
| `offline` | Constant .22 | Still | none |

- **Implementation:** pure SVG with SMIL `<animate>`, so no JavaScript runs per frame. With `prefers-reduced-motion: reduce`, render the still pose: the idle mood at full opacity, with no rings and no bubble.
- **Accessibility:** `role="img"` with an `aria-label` such as "Robot avatar atlas-005a, thinking". When the bot's name is shown next to it, mark the avatar `aria-hidden`.
- **Sizes in the app:** 22–28 (status lines), 30–36 (chat, sidebar), 64 (cards, bot header), 96–112 (profile, builder).

## 9. Iconography
- Inline stroke icons on a 24 px grid, `stroke="currentColor"`, width 2–2.4, round caps and joins, `fill="none"`. Sizes 14 / 16 / 18 / 20.
- The icons in the designs follow Lucide geometry. Use `lucide-react` in the app: bell, user, search, plus, arrow-left, shield, key, ban, terminal, monitor, clipboard, camera, maximize, paperclip, send, trash, rotate-cw, lock, folder, sliders-horizontal, git-merge, info, check, x, pencil.
- Decorative icons get `aria-hidden="true"`. Icon-only buttons get an `aria-label`.
- No emoji in the UI.

## 10. Motion
- **Transitions:** 0.18 s ease for colour, border and transform.
- **Hover:** buttons lift 1 px, cards lift 4 px with `shadow-hover`, arrows move 3 px.
- **Live state:** avatar moods (section 8), and a subtle fade for new messages. Do not animate layout or auto-scroll while the user is reading older messages.
- **Reduced motion:** honour `prefers-reduced-motion: reduce` everywhere: disable transitions and SMIL animations.

## 11. Accessibility
- **Contrast:** at least 4.5:1 for text, 3:1 for large text (24 px and up) and UI boundaries. The pairs in section 3 are checked; do not put `green` or `on-dark-faint` behind small text.
- **Focus:** `:focus-visible` gets a 2 px `green` outline with a 2 px offset. Never remove it.
- **Semantics:** use real `<button>`, `<a href>`, `<input>` + `<label>`, `role="switch"` for switches and `aria-pressed` for toggles. Use `role="log"` for message lists and `role="dialog"` with `aria-modal` for dialogs.
- **Never colour alone:** statuses always have a text label, and blocked or failed states use amber plus a text label.
- **Keyboard:** Enter sends and Shift+Enter adds a newline in composers. Escape closes dialogs. Every action is reachable by keyboard.

## 12. Voice and copy
- **English, sentence case,** short and concrete ("Stop sandbox", "Add secret", "Bot-to-bot on").
- Bots speak in the first person and in plain words ("Rebased, 25 tests green.").
- Name the mechanism when it builds trust: "Token added by the egress proxy. Atlas never saw it."
- Show times as `14:32`, durations as `2 h 14 min` and sizes as `GiB`, formatted via `Intl` for the active locale.

## 13. Internationalisation
- The UI ships in English first, and every string goes through `next-intl` (`messages/en.json`) from day one.
- Use ICU messages for plurals and variables. Never concatenate translated fragments.
- Leave about 30 % extra room for longer languages (German). Avoid fixed widths on labels and buttons.
- The API returns error **codes**, and the frontend translates them.
- Bot output is not translated. The reply language is a per-bot setting.

## 14. Implementation notes

Expose the tokens as CSS variables and map them in the Tailwind config, for example:

```ts
// tailwind.config.ts (excerpt)
theme: {
  extend: {
    colors: {
      ink: { DEFAULT: "#182019", 2: "#3a4a3c" },
      muted: "#5d6b60",
      paper: { DEFAULT: "#ffffff", 2: "#f5f8f4" },
      line: { DEFAULT: "#e3ebe2", strong: "#c9d8c9" },
      green: { DEFAULT: "#3f9147", 600: "#347a3b", light: "#5eab60", logo: "#49b24d" },
      mint: { DEFAULT: "#d6fcd0", soft: "#eef8ec", line: "#cfe0cf" },
      dark: { DEFAULT: "#2a3c2a", 2: "#1f2d1f" },
      terminal: "#14201a",
      blue: { DEFAULT: "#4a5e92", tint: "#edf0f6", line: "#d3d9e8" },
      hex: { 1: "#8aa8db", 2: "#6d8fce", 3: "#3f5f9c" },
      amber: { DEFAULT: "#b45309", tint: "#fdf3e7", line: "#e8b98a", "on-dark": "#f0b46a" },
    },
    borderRadius: { xs: "6px", sm: "9px", md: "12px", lg: "14px" },
    boxShadow: {
      card: "0 1px 2px rgba(26,44,26,.06), 0 8px 24px rgba(26,44,26,.07)",
      "card-hover": "0 6px 16px rgba(26,44,26,.10), 0 18px 40px rgba(26,44,26,.12)",
    },
    fontFamily: {
      sans: ["var(--font-lato)", "system-ui", "sans-serif"],
      mono: ["ui-monospace", "SFMono-Regular", "SF Mono", "Menlo", "Consolas", "Liberation Mono", "monospace"],
    },
  },
}
```

- Port the avatar as a React component (`<BotAvatar seed mood size tile />`) from [`source/Avatar.dc.html`](source/Avatar.dc.html), and write a Go test that checks the hash and part selection against the same fixtures.
- The `.dc.html` files in [`source/`](source/) are reference markup from the design canvas, with exact inline styles. They need the canvas runtime and are not meant to run in the app.

### Bot workspace loading & fullscreen desktop
- **Workspace loading state:** `/bots/:id` renders a structural zero-layout-shift skeleton matching the split chat + computer layout, paired with a centered `dark-2` loading card inside the 16:10 `terminal` (`#14201a`) viewport showing the `bot.Box` hexagon-robot emblem, accessible `role="status"` text, and progress indicator (static under `prefers-reduced-motion: reduce`).
- **Desktop fullscreen overlay:** Expanding the live computer opens a full-viewport `terminal` (`#14201a`) overlay while keeping the active noVNC RFB session mounted (no socket reconnect when toggling). The pinned `dark-2` (`#1f2d1f`) toolbar exposes status, `Take control` / hand-back note + `Hand back`, a toggleable slide-over `Chat` drawer, optional browser `requestFullscreen()`, and `Exit fullscreen` (`Esc`), all with ≥ 44 px touch targets and `whitespace-nowrap` labels.

### Bot stop and pause controls

Direct messages and group chats show **Stop response** beside each running
bot's activity. In a group, the button's accessible name identifies the bot.
The secondary button targets that turn even when newer messages are queued.
It changes to **Stopping…** while the request is pending. A failed request
shows an amber inline alert and lets the user retry.

The workspace header and bot settings expose Pause bot / Resume bot to bot managers, with the
persisted state readable by every workspace role. Use the secondary button style,
44 px touch targets, and explicit text labels. Paused bots show that queued
messages keep their order and interrupted turns are not replayed. A failed control
request shows an amber inline alert and reloads the stored state, because a pause
may have committed before cancellation transport failed.

Budget displays count proxy request records once. A CLI aggregate must not add a
second charge to already metered requests. Null cost on metered requests means unavailable pricing or
unreconciled usage, rather than zero spend. Subscription records show tokens-only
usage and must not be labeled missing billing. Use an explicit warning for blocked
budget admission; raw provider errors and credentials never appear in chat.

Chat turn failures use specific, translated recovery hints when the server knows
the cause. Computer failures link to the workspace computer controls; missing
provider credentials link to the bot Secrets page. Keep the amber warning style,
visible underlined links and 44 px targets. Unknown codes use the generic message.
Never interpolate raw dependency errors or unknown codes into user-facing text.
CLI stream format failures explain that bot.Box could not read the response and
suggest another message, then administrator help with CLI compatibility if it
persists. Keep this separate from provider, credential and budget recovery hints.

Claude CLI bots show a Pro / Max setup card on Secrets & network. It prepares the
write-only secret form with the fixed credential name, provider host and Bearer
scheme, while preserving existing role permissions. Display the official setup
command and optional project helper as code. The token itself remains a password
input, is cleared on save, and is never rendered from a server response.

### Egress blocklist, triage & pending enforcement

The bot **Secrets & network** view (`#31`, Stitch screen `9134a93a74a544c48764366fbaac88ff`) pairs **Allowed hosts** with **Blocked hosts** using dashed `amber-line` / `amber-tint` chips (`amber` text, zero red). Inside the dark `terminal` **Recent egress activity** log:

- Action buttons (**Allow**, **Block**, **Undo**, **Edit rule**) keep ≥ 44 px touch targets and explicit text labels.
- **Pending enforcement** (`202 Accepted` while waiting on the running proxy) uses dashed `amber-line` / `amber-on-dark` (`Saved · proxy pending`) and never displays an `applied` badge (`mint-soft` / `green-600`) until proxy acknowledgement is confirmed.
- **Conflicts & unknown state** (`409 policy_conflict`, Undo divergence, or failed reconciliation after an interrupted save) surface translated `amber-tint` alerts; failed reconciliation locks further policy writes behind an explicit **Reload policy** action until authoritative state reloads.

### Self-hosted GitLab login destinations

Workspace settings show named GitLab instances with their complete canonical
HTTPS origin in monospace. Owners and admins can add and remove instances; members
and viewers see the destinations without mutation controls. Bot settings offer labeled, 44 px checkbox targets to assign zero or more registered
instances. Explain that explicit assignment and bot egress permission are both required;
no manual IP configuration or restart is needed. Removing a bot assignment revokes only
that bot's affected sessions and pending sign-ins. Use the existing
Card, Input, Button and Alert components, including 44 px targets and visible
focus. Removal copy states that active sessions are revoked and re-adding requires
a new login. Secret dialogs continue to display the complete destination origin;
credentials never appear in instance settings or chat cards.

### GitLab CLI credential recovery

The existing GitLab instance and bot-assignment panels cover browser, REST API and
Git HTTPS access. Removing access explains that sessions, token grants and pending
requests are revoked. Secret cards show the exact canonical origin and adapter.
Group secret and broker dialogs name the bot that sent the selected request.
Eligible saved GitLab credentials are tried without opening a secret card.
Only non-secret placeholders may appear in CLI continuation guidance. Failed
credential requests use specific next-intl messages for registration, assignment,
network permission, invalid input, limits and policy availability instead of the
generic model/computer failure. Never render dependency errors or secret previews.
Successful fulfillment resumes the existing turn immediately, including when the
user submits the credential shortly after the card appears.

Git transfer size failures are operational limits, not missing host permission or
rejected authentication. The `gitlab_git` response cap is 64 MiB; API adapters
retain 2 MiB (ADR-0023). Recovery copy must distinguish size limits from interrupted
upstream reads and must not suggest repeated credential requests, expanding host
permissions or fetching archives to bypass mediation. Show only safe failure
details, never upstream error text that could reflect credentials.

### Bot computer housekeeping

Bots maintain their own persistent computer and decide routine local cleanup
without confirmation prompts. They may briefly report meaningful cleanup in the
conversation; trivial file-management decisions should not interrupt the user.
Questions should identify an unresolved risk to valuable work, shared data or
user-provided files. This conversational guidance adds no approval dialog or
permission control; external actions follow the user's authorization and existing
infrastructure limits still apply. See ADR-0024.

### MCP proxy & command permissions

Workspace Settings lists registered HTTPS MCP servers with monospace slugs, canonical
endpoints, masked write-only auth previews (`••••••••`), and discovered tool counts.
Bot settings / Secrets & MCP displays assigned servers with a three-way segmented
control per command (`Allow` in `mint-soft`/`green-600`, `Ask approval` in
`blue-tint`/`blue`, and `Block` in `amber-tint` with dashed `amber-line` — zero red).
Unconfigured tools remain blocked by default (`fail-closed`). Maintain 44 px touch
targets, `role="switch"` for server assignment, `aria-pressed` on policy segments,
and dark `terminal` (`#14201a`) cards for runner wiring and `Recent MCP proxy activity`.

### Codex connection and subscription budgets

- Put **ChatGPT plan for Codex** on **Secrets & network**. Show the connection
  status in text. Label the one-time pairing code as a read-only input and show
  the workstation command only during pairing.
- Put **Count subscription usage toward budgets** with the workspace budget
  controls. Label provider spend and internal subscription charges separately.
- Show input, output, and cached-input token totals by model and period. Use a
  text status for usage awaiting reconciliation.

### Bot knowledge base & in-chat memory cards

- In DM and group chats, bot-saved memories render an inline `Memory saved` card
  (`mint-line` border, or `paper-2` / `line` when removed via Undo) showing the
  injection mode pill (`Always in context` in `mint-soft`/`green-600`, `On-demand`
  in `blue-tint`/`blue`), title, content, tag chips, source/token metadata, `Edit`,
  amber `Undo`, and `Open in Knowledge base →`.
- Bot Settings exposes `/bots/:id/settings/knowledge` inside `BotSettingsShell`
  with an `Auto-save memories from chat` switch, `Always-in-context` 4,000-token
  progressbar (80 % tick, amber above 80 %), dark `terminal` zero-secret guard
  card, search/mode/source/tag filters, inline `Always` / `On-demand` toggles,
  modal Add/Edit form, and dashed `amber-line` **Clear knowledge base** danger zone.

### LLM proxy guardrails & in-chat feedback

- **Workspace Settings (`/settings/guardrails`, Stitch `3fa18a9575b94b11be1326347f297f9d`):**
  Uses `WorkspaceSettingsShell` with an opt-in master policy card (`Off · opt-in` or `Enforcing · N active`, `fail_closed` / `fail_open` failure mode), three managed built-in detector cards (**Secrets & credentials**, **PII protection** with `email`/`iban`/`card`/`phone` entity chips, and **Prompt injection**), a custom RE2 regex & keyword rule table plus interactive **Quick Test** panel, and a dark `terminal` (`#14201a`) sanitized audit stream (`llmproxy` decision log) showing redacted (`blue-tint`/`blue`), blocked (`amber-tint`/`amber`, zero red), audited (`mint-soft`/`green-600`), and allowed entries without ever displaying raw secret or PII values. Workspace members and viewers see a read-only banner with no mutation buttons or forms.
- **In-chat & Tool calls feedback (Stitch `4d11e1714a994caab20911490820d37c`):**
  Both direct messages and group chats render inline `GuardrailNoticeCard` notices for `kind = 'guardrail'` rows: **Redacted** interventions use `blue-tint`, `blue-line`, a `Shield` icon, match pill (`Input/Output · N matches`), and `Inspect in Tool calls →`; **Blocked · 403** interventions use `amber-tint`, `1.5 px` dashed `amber-line`, `ShieldAlert` (zero red), rule/direction/provider metadata, and show `Open Guardrails settings →` (`/settings/guardrails`) only for `owner` and `admin`. Inside `ToolCallsSidebar`, guardrail trace steps render specialized left-accent `blue` (redacted) or dashed `amber` (blocked) step cards with rule/entity chips, millisecond latency, and a monospace `route · model · action · raw values scrubbed` footer strip; `ChatTurnToolChip` appends redacted/blocked counts; and `BotWorkspaceView` displays a header status chip linking to `/settings/guardrails`.
