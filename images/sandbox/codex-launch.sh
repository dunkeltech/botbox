#!/bin/bash
# SPDX-License-Identifier: AGPL-3.0-only

set -euo pipefail

codex_real=/usr/local/bin/codex-real
proxy="${HTTP_PROXY:-${http_proxy:-}}"
proxy="${proxy%/}"

botbox_codex() {
  local provider="$1" key="$2" base_url="$3"
  shift 3
  local proxy_host="${proxy#*://}"
  proxy_host="${proxy_host%%:*}"
  local no_proxy_hosts="${NO_PROXY:-${no_proxy:-}}"
  export NO_PROXY="${no_proxy_hosts:+$no_proxy_hosts,}$proxy_host"
  export no_proxy="$NO_PROXY"

  # The metered proxy carries HTTP/SSE. The newer Codex built-in provider
  # opens a native WebSocket even when an API base URL is set.
  exec "$codex_real" \
    -c "model_provider=\"$provider\"" \
    -c "model_providers.$provider.name=\"bot.Box\"" \
    -c "model_providers.$provider.base_url=\"$base_url\"" \
    -c "model_providers.$provider.env_key=\"$key\"" \
    -c "model_providers.$provider.wire_api=\"responses\"" \
    -c "model_providers.$provider.requires_openai_auth=false" \
    -c "model_providers.$provider.supports_websockets=false" \
    -c "model_providers.$provider.capabilities.remote_compaction=\"unsupported\"" \
    -c 'features.remote_models=false' "$@"
}

botbox_openai_auth_mode() {
  curl --noproxy '*' --silent --fail --max-time 5 "$proxy/llm/openai/auth-mode" 2>/dev/null | tr -d '[:space:]' || true
}

if [[ -z "$proxy" ]]; then
  exec "$codex_real" "$@"
fi
case "${1:-}" in
  exec|e)
    if [[ "${OPENAI_API_KEY:-}" == botbox-placeholder &&
          "${OPENAI_BASE_URL:-}" == "$proxy/llm/openai/v1" ]]; then
      if [[ "$(botbox_openai_auth_mode)" == "oauth" ]]; then
        botbox_codex botbox_api OPENAI_API_KEY "$OPENAI_BASE_URL" \
          -c 'features.tool_search=false' \
          -c 'web_search="disabled"' "$@"
      fi
      botbox_codex botbox_api OPENAI_API_KEY "$OPENAI_BASE_URL" "$@"
    fi
    exec "$codex_real" "$@"
    ;;
  --version|-V|--help|-h|help|completion|mcp|app-server)
    exec "$codex_real" "$@"
    ;;
  login|logout)
    echo "Manage the ChatGPT plan in the bot's Secrets & network page." >&2
    exit 1
    ;;
esac
if ! curl --noproxy '*' --silent --fail --max-time 5 "$proxy/llm/openai/ready" >/dev/null; then
  echo "Connect a ChatGPT plan or OpenAI key in bot.Box and take control of this bot computer." >&2
  exit 1
fi

# Both interactive desktop and chat turns share the unified /llm/openai/v1 route.
export BOTBOX_CODEX_PLACEHOLDER=botbox-placeholder
# Docker provides the filesystem and network boundary. The nested bubblewrap
# sandbox needs user namespaces that the bot container does not allow.
if [[ "$(botbox_openai_auth_mode)" == "api_key" ]]; then
  botbox_codex botbox_openai BOTBOX_CODEX_PLACEHOLDER "$proxy/llm/openai/v1" \
    --sandbox danger-full-access \
    --ask-for-approval on-request "$@"
fi
botbox_codex botbox_openai BOTBOX_CODEX_PLACEHOLDER "$proxy/llm/openai/v1" \
  --sandbox danger-full-access \
  --ask-for-approval on-request \
  -c 'model="gpt-6.1-sol"' \
  -c 'features.tool_search=false' \
  -c 'web_search="disabled"' "$@"
