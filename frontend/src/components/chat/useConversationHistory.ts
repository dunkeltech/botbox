// SPDX-License-Identifier: AGPL-3.0-only

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { components } from '@/types/openapi.gen';

type History = components['schemas']['ChatHistory'];
type ChatMessage = History['messages'][number];

const FALLBACK_PAGE_SIZE = 50;
const INITIAL_VISIBLE_TARGET = 15;
const MAX_PAGES_PER_BATCH = 5;
const MAX_INITIAL_BACKFILL_PAGES = 4;

function pageHasMore(page: History): boolean {
  if (typeof page.hasMore === 'boolean') {
    return page.hasMore;
  }
  return page.messages.length >= FALLBACK_PAGE_SIZE;
}

function mergeMessages(
  archived: ChatMessage[],
  latest: ChatMessage[],
): ChatMessage[] {
  if (archived.length === 0 && latest.length === 0) return [];
  const byId = new Map<string, ChatMessage>();
  for (const msg of archived) {
    byId.set(msg.id, msg);
  }
  for (const msg of latest) {
    byId.set(msg.id, msg);
  }
  return Array.from(byId.values()).sort((a, b) => a.position - b.position);
}

interface ArchiveState {
  key: string;
  messages: ChatMessage[];
  olderHasMore: boolean | null;
}

export interface UseConversationHistoryResult {
  /** All loaded raw messages (oldest to newest, deduplicated by id). */
  rawMessages: ChatMessage[];
  /** Whether older messages remain before the earliest loaded message. */
  hasMore: boolean;
  /** True while an older-history request is in flight. */
  loadingOlder: boolean;
  /** Load the next older page(s) before the earliest loaded message. */
  loadOlder: () => Promise<void>;
}

/**
 * Accumulates paginated conversation messages across live refreshes and
 * `before`-cursor prepends so polling never drops previously loaded history.
 */
export function useConversationHistory({
  conversationKey,
  latestPage,
  fetchOlderPage,
  preparePrepend,
  onError,
}: {
  conversationKey: string;
  latestPage: History | undefined;
  fetchOlderPage: (beforePosition: number) => Promise<History>;
  preparePrepend: () => void;
  onError?: () => void;
}): UseConversationHistoryResult {
  const [archive, setArchive] = useState<ArchiveState>({
    key: conversationKey,
    messages: [],
    olderHasMore: null,
  });
  const [loadingOlder, setLoadingOlder] = useState(false);

  const loadingRef = useRef(false);
  const generationRef = useRef(0);
  const backfilledKeyRef = useRef('');
  const preparePrependRef = useRef(preparePrepend);
  preparePrependRef.current = preparePrepend;
  const fetchOlderRef = useRef(fetchOlderPage);
  fetchOlderRef.current = fetchOlderPage;
  const onErrorRef = useRef(onError);
  onErrorRef.current = onError;

  // Reset archived pages when switching to a different DM or group.
  useEffect(() => {
    generationRef.current += 1;
    loadingRef.current = false;
    setLoadingOlder(false);
    setArchive({
      key: conversationKey,
      messages: [],
      olderHasMore: null,
    });
  }, [conversationKey]);

  // Persist every message seen from the latest polling window so sliding
  // windows caused by new incoming messages never create gaps.
  useEffect(() => {
    if (!conversationKey || !latestPage?.messages.length) return;
    const incoming = latestPage.messages;
    setArchive((prev) => {
      const base = prev.key === conversationKey ? prev.messages : [];
      const merged = mergeMessages(base, incoming);
      if (
        prev.key === conversationKey &&
        merged.length === base.length &&
        merged.every((m, idx) => m === base[idx])
      ) {
        return prev;
      }
      return {
        key: conversationKey,
        messages: merged,
        olderHasMore: prev.key === conversationKey ? prev.olderHasMore : null,
      };
    });
  }, [conversationKey, latestPage]);

  const rawMessages = useMemo(() => {
    const base = archive.key === conversationKey ? archive.messages : [];
    return mergeMessages(base, latestPage?.messages ?? []);
  }, [archive, conversationKey, latestPage?.messages]);

  const olderHasMore =
    archive.key === conversationKey ? archive.olderHasMore : null;
  const hasMore =
    olderHasMore !== null
      ? olderHasMore
      : latestPage
        ? pageHasMore(latestPage)
        : false;

  const rawMessagesRef = useRef(rawMessages);
  rawMessagesRef.current = rawMessages;
  const hasMoreRef = useRef(hasMore);
  hasMoreRef.current = hasMore;

  const fetchBatch = useCallback(
    async ({
      preserveScroll,
      maxPages,
      stopWhenVisibleCountAtLeast,
    }: {
      preserveScroll: boolean;
      maxPages: number;
      stopWhenVisibleCountAtLeast?: number;
    }) => {
      if (!conversationKey || loadingRef.current || !hasMoreRef.current) return;
      const first = rawMessagesRef.current[0];
      if (!first) return;

      const gen = generationRef.current;
      loadingRef.current = true;
      setLoadingOlder(true);

      try {
        let cursor = first.position;
        let more: boolean = hasMoreRef.current;
        const collected: ChatMessage[] = [];
        let visibleCollected = 0;
        const initialVisible = rawMessagesRef.current.filter(
          (m) => m.kind !== 'tool',
        ).length;

        for (let i = 0; i < maxPages && more; i += 1) {
          const page = await fetchOlderRef.current(cursor);
          if (generationRef.current !== gen) return;

          more = pageHasMore(page);
          if (page.messages.length === 0) {
            more = false;
            break;
          }
          collected.push(...page.messages);
          visibleCollected += page.messages.filter(
            (m) => m.kind !== 'tool',
          ).length;

          const nextCursor = page.messages[0]?.position;
          if (nextCursor === undefined || nextCursor >= cursor) {
            more = false;
            break;
          }
          cursor = nextCursor;

          if (stopWhenVisibleCountAtLeast !== undefined) {
            if (
              initialVisible + visibleCollected >=
              stopWhenVisibleCountAtLeast
            ) {
              break;
            }
          } else if (visibleCollected > 0) {
            break;
          }
        }

        if (generationRef.current !== gen) return;
        if (preserveScroll && collected.length > 0) {
          preparePrependRef.current();
        }
        setArchive((prev) => {
          const base = prev.key === conversationKey ? prev.messages : [];
          return {
            key: conversationKey,
            messages: mergeMessages(base, collected),
            olderHasMore: more,
          };
        });
      } catch {
        if (generationRef.current === gen) {
          onErrorRef.current?.();
        }
      } finally {
        if (generationRef.current === gen) {
          loadingRef.current = false;
          setLoadingOlder(false);
        }
      }
    },
    [conversationKey],
  );

  // If the newest DB window is dominated by tool rows, silently backfill a few
  // older pages on initial load so the user sees a useful transcript immediately.
  useEffect(() => {
    if (!conversationKey || !latestPage) return;
    if (backfilledKeyRef.current === conversationKey) return;
    backfilledKeyRef.current = conversationKey;

    const visibleCount = latestPage.messages.filter(
      (m) => m.kind !== 'tool',
    ).length;
    if (pageHasMore(latestPage) && visibleCount < INITIAL_VISIBLE_TARGET) {
      void fetchBatch({
        preserveScroll: false,
        maxPages: MAX_INITIAL_BACKFILL_PAGES,
        stopWhenVisibleCountAtLeast: INITIAL_VISIBLE_TARGET,
      });
    }
  }, [conversationKey, latestPage, fetchBatch]);

  const loadOlder = useCallback(
    () =>
      fetchBatch({
        preserveScroll: true,
        maxPages: MAX_PAGES_PER_BATCH,
      }),
    [fetchBatch],
  );

  return {
    rawMessages,
    hasMore,
    loadingOlder,
    loadOlder,
  };
}
