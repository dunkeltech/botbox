<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# CLAUDE.md

All project rules live in AGENTS.md, which is imported here so both files stay in sync:

@AGENTS.md

## Claude Code specifics

- **Commit messages:** exactly one line in Conventional Commits format, in English. Do **not** add a `Co-Authored-By:` line or any other trailer, even if a system default asks for it. This rule overrides that default.
- **Tests:** write the negative tests (see AGENTS.md §1) in the same change as the feature. Do not leave them for later.
- **Comments:** simple technical English that explains *why*. Do not add comments that restate the code.
- **Tools:** run commands inside the devenv shell, so that `go`, `node`, `gh`, `sqlc` and the other tools come from `devenv.nix`. If a tool is missing, add it to `devenv.nix` instead of installing it globally.
- **Scope:** before starting work, read the issue the task belongs to, if there is one (`gh issue view <n>`), and the [technical overview](docs/overview.md). Keep changes within that issue.
