// SPDX-License-Identifier: AGPL-3.0-only

import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  type RefObject,
} from 'react';

const BOTTOM_THRESHOLD_PX = 48;
const TOP_THRESHOLD_PX = 16;

function isNearBottom(element: HTMLElement): boolean {
  return (
    element.scrollHeight - element.scrollTop - element.clientHeight <=
    BOTTOM_THRESHOLD_PX
  );
}

export interface UseChatScrollOptions {
  /** Resets pin-to-bottom state when switching conversations or groups. */
  conversationKey: string;
  /** Number of rendered messages in the log. */
  messageCount: number;
  /** ID of the oldest rendered message, used to detect prepended pages. */
  firstMessageId?: string;
  /** ID of the newest rendered message, used to detect appended items. */
  lastMessageId: string;
  /** Live streaming reply text, if any. */
  liveText?: string;
  /** Called when the reader scrolls to the top of a scrollable history log. */
  onReachTop?: () => void;
}

export interface UseChatScrollResult {
  /** Ref attached to the scrollable `role="log"` message container. */
  logRef: RefObject<HTMLDivElement | null>;
  /** Updates whether auto-scroll should stay pinned when the user scrolls. */
  onScroll: () => void;
  /** Keeps the log pinned when async child media (e.g. screenshots) finishes loading. */
  onMediaLoad: () => void;
  /** Forces pin-to-bottom and scrolls immediately (e.g. after sending a message). */
  scrollToBottom: () => void;
  /** Captures the current scroll height before older messages are prepended. */
  preparePrepend: () => void;
}

/**
 * Keeps a chat log scrolled to the newest message on initial load, conversation
 * switches, streaming updates, and newly sent messages, while pausing auto-scroll
 * when the user scrolls up to read older history.
 */
export function useChatScroll({
  conversationKey,
  messageCount,
  firstMessageId = '',
  lastMessageId,
  liveText = '',
  onReachTop,
}: UseChatScrollOptions): UseChatScrollResult {
  const logRef = useRef<HTMLDivElement | null>(null);
  const stickToBottomRef = useRef(true);
  const prevConversationKeyRef = useRef(conversationKey);
  const prependAnchorRef = useRef<{
    scrollHeight: number;
    scrollTop: number;
  } | null>(null);
  const onReachTopRef = useRef(onReachTop);
  onReachTopRef.current = onReachTop;

  useLayoutEffect(() => {
    if (prevConversationKeyRef.current !== conversationKey) {
      prevConversationKeyRef.current = conversationKey;
      stickToBottomRef.current = true;
      prependAnchorRef.current = null;
    }

    const element = logRef.current;
    if (!element) return;

    if (prependAnchorRef.current) {
      const delta =
        element.scrollHeight - prependAnchorRef.current.scrollHeight;
      element.scrollTop =
        prependAnchorRef.current.scrollTop + Math.max(0, delta);
      prependAnchorRef.current = null;
      return;
    }

    if (stickToBottomRef.current) {
      element.scrollTop = element.scrollHeight;
    }
  }, [conversationKey, messageCount, firstMessageId, lastMessageId, liveText]);

  useEffect(() => {
    const element = logRef.current;
    if (!element || typeof ResizeObserver === 'undefined') return;

    // Keep the newest message visible when surrounding layout shifts
    // (for example, opening the mention picker or typing indicator).
    const observer = new ResizeObserver(() => {
      if (stickToBottomRef.current) {
        element.scrollTop = element.scrollHeight;
      }
    });
    observer.observe(element);
    return () => observer.disconnect();
  }, []);

  const onScroll = useCallback(() => {
    const element = logRef.current;
    if (!element) return;
    const nearBottom = isNearBottom(element);
    stickToBottomRef.current = nearBottom;
    // Only auto-load older messages when the log actually overflows and the
    // reader intentionally scrolled up to the top edge.
    if (
      !nearBottom &&
      element.scrollHeight > element.clientHeight &&
      element.scrollTop <= TOP_THRESHOLD_PX
    ) {
      onReachTopRef.current?.();
    }
  }, []);

  const onMediaLoad = useCallback(() => {
    const element = logRef.current;
    if (element && stickToBottomRef.current) {
      element.scrollTop = element.scrollHeight;
    }
  }, []);

  const scrollToBottom = useCallback(() => {
    stickToBottomRef.current = true;
    const element = logRef.current;
    if (element) {
      element.scrollTop = element.scrollHeight;
    }
  }, []);

  const preparePrepend = useCallback(() => {
    const element = logRef.current;
    if (!element) return;
    // Prepending older messages must preserve the reader's viewport position
    // rather than jumping back to the bottom on the next layout commit.
    stickToBottomRef.current = false;
    prependAnchorRef.current = {
      scrollHeight: element.scrollHeight,
      scrollTop: element.scrollTop,
    };
  }, []);

  return {
    logRef,
    onScroll,
    onMediaLoad,
    scrollToBottom,
    preparePrepend,
  };
}
