// SPDX-License-Identifier: AGPL-3.0-only

/** Reconnect a private conversation and reconcile snapshots after every reconnect. */
export function connectConversation(
  workspaceId: string,
  conversationId: string,
  token: () => string | null,
  onMessage: (event: MessageEvent<string>) => void,
  onOpen: () => void,
): () => void {
  let stopped = false;
  let socket: WebSocket | undefined;
  let retry: ReturnType<typeof setTimeout> | undefined;
  function connect() {
    const accessToken = token();
    if (!accessToken) return;
    const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
    const connected = new WebSocket(
      `${protocol}//${location.host}/ws/chat/${encodeURIComponent(workspaceId)}/${encodeURIComponent(conversationId)}`,
      ['botbox.events', `botbox.auth.${accessToken}`],
    );
    socket = connected;
    connected.onmessage = (event: MessageEvent<unknown>) => {
      if (typeof event.data !== 'string') return;
      if (!stopped && socket === connected)
        onMessage(event as MessageEvent<string>);
    };
    connected.onopen = () => {
      if (!stopped && socket === connected) onOpen();
    };
    connected.onclose = () => {
      if (!stopped && socket === connected) retry = setTimeout(connect, 1000);
    };
  }
  connect();
  return () => {
    stopped = true;
    clearTimeout(retry);
    socket?.close();
  };
}
