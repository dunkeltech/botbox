#!/bin/bash
# Interactive Claude Code 2.1.323 checks api.anthropic.com and
# platform.claude.com, then asks the person to trust the working directory.
# Provider tunnels stay closed and nobody can answer that prompt in the
# sandbox. Present the placeholder that matches the stored credential and use
# the metered proxy instead. A login helper without HTTP_PROXY keeps the
# official binary.

# The npm package postinstall copies the native ELF over this path.
claude_real="/usr/local/lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe"

botbox_exec_claude() {
  # A text file here is the install stub. Running it would look like a
  # successful command that cannot talk to the provider.
  local sig
  sig="$(head -c 4 "$claude_real" 2>/dev/null || true)"
  if [[ "$sig" != $'\x7fELF' ]]; then
    echo "Claude native binary is not installed." >&2
    exit 1
  fi
  exec "$claude_real" "$@"
}

botbox_fetch_auth_mode() {
  local proxy="$1" out code
  out="$(mktemp)"
  code="$(curl --noproxy '*' --silent --show-error --max-time 5 -o "$out" -w '%{http_code}' "${proxy}/llm/anthropic/auth-mode" || true)"
  if [[ "$code" != "200" ]]; then
    rm -f "$out"
    printf 'unavailable'
    return 0
  fi
  tr -d '[:space:]' <"$out"
  rm -f "$out"
}

botbox_skip_claude_onboarding() {
  # 2.1.323 ignores ANTHROPIC_BASE_URL until onboarding and the trust
  # prompt are done. Mark both done so the desktop CLI uses the metered
  # route. The approved value is the public placeholder.
  python3 - <<'PY'
import json, os
path = os.path.join(os.environ["HOME"], ".claude.json")
data = {}
try:
    with open(path) as handle:
        loaded = json.load(handle)
    if isinstance(loaded, dict):
        data = loaded
except FileNotFoundError:
    pass
except json.JSONDecodeError:
    data = {}
data["hasCompletedOnboarding"] = True
if not data.get("theme"):
    data["theme"] = "dark"
# 2.1.323 stops on a trust prompt after onboarding. The desktop has no
# separate confirmation step, so the directory where claude was started is
# marked trusted. Other project settings are kept.
projects = data.get("projects")
if not isinstance(projects, dict):
    projects = {}
    data["projects"] = projects
cwd = os.getcwd()
project = projects.get(cwd)
if not isinstance(project, dict):
    project = {}
    projects[cwd] = project
project["hasTrustDialogAccepted"] = True
responses = data.get("customApiKeyResponses")
if not isinstance(responses, dict):
    responses = {}
    data["customApiKeyResponses"] = responses
approved = responses.get("approved")
if not isinstance(approved, list):
    approved = []
    responses["approved"] = approved
if "botbox-placeholder" not in approved:
    approved.append("botbox-placeholder")
temporary = path + ".tmp"
with open(temporary, "w") as handle:
    json.dump(data, handle)
os.replace(temporary, path)
PY
}

botbox_prepare_claude() {
  if [[ -n "${CLAUDE_CODE_OAUTH_TOKEN:-}" || -n "${ANTHROPIC_API_KEY:-}" || -n "${ANTHROPIC_AUTH_TOKEN:-}" ]]; then
    return 0
  fi
  local proxy="${HTTP_PROXY:-${http_proxy:-}}"
  proxy="${proxy%/}"
  if [[ -z "$proxy" ]]; then
    echo "Claude has no metered proxy in this computer." >&2
    return 1
  fi
  local mode
  mode="$(botbox_fetch_auth_mode "$proxy")"
  case "$mode" in
    oauth)
      export CLAUDE_CODE_OAUTH_TOKEN=botbox-placeholder
      unset ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN
      ;;
    api_key)
      export ANTHROPIC_API_KEY=botbox-placeholder
      unset CLAUDE_CODE_OAUTH_TOKEN ANTHROPIC_AUTH_TOKEN
      ;;
    unavailable)
      echo "Claude credential service is unavailable." >&2
      return 1
      ;;
    *)
      echo "No Claude credential is configured for this computer." >&2
      return 1
      ;;
  esac
  export ANTHROPIC_BASE_URL="${proxy}/llm/anthropic"
  # Reach the proxy directly. Sending this HTTP route back through CONNECT
  # would hit the provider-tunnel denial.
  case ",${NO_PROXY:-${no_proxy:-}}," in
    *,egress,*) ;;
    *)
      if [[ -n "${NO_PROXY:-${no_proxy:-}}" ]]; then
        export NO_PROXY="${NO_PROXY:-$no_proxy},egress"
      else
        export NO_PROXY="egress"
      fi
      export no_proxy="$NO_PROXY"
      ;;
  esac
  # statsig.anthropic.com is a provider host and its tunnel is closed.
  export DISABLE_TELEMETRY=1
  export CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
  botbox_skip_claude_onboarding
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  set -euo pipefail
  if [[ -z "${CLAUDE_CODE_OAUTH_TOKEN:-}" && -z "${ANTHROPIC_API_KEY:-}" && -z "${ANTHROPIC_AUTH_TOKEN:-}" && -z "${HTTP_PROXY:-${http_proxy:-}}" ]]; then
    botbox_exec_claude "$@"
  fi
  botbox_prepare_claude
  botbox_exec_claude "$@"
fi
