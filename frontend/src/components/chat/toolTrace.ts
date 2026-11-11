import {
  describeToolMessage,
  type ToolCopy,
} from '@/components/chat/toolMessage';
import type { components } from '@/types/openapi.gen';

export type ToolChatMessage = components['schemas']['ChatMessage'];
export type ToolChatTurn = components['schemas']['ChatTurn'];

export interface ToolTraceStep {
  id: string;
  turnKey: string;
  toolCallId?: string;
  botId?: string;
  callCopy: ToolCopy;
  resultCopy?: ToolCopy;
  status: 'running' | 'done' | 'error';
  errorCode?: string;
  startedAt: string;
  finishedAt?: string;
  durationMs: number | null;
  imagePath?: string;
  guardrail?: components['schemas']['ChatMessageGuardrail'];
}

export interface ToolTraceTurn {
  turnKey: string;
  botId?: string;
  startedAt: string;
  durationMs: number | null;
  status: 'running' | 'done' | 'error';
  steps: ToolTraceStep[];
  replyMessageId?: string;
  anchorAfterMessageId?: string;
}

export interface ToolTraceSummary {
  chatMessages: ToolChatMessage[];
  /** Turns ordered newest-first for sidebar display. */
  turns: ToolTraceTurn[];
  totalSteps: number;
  runningSteps: number;
  errorSteps: number;
  turnsByReplyId: Map<string, ToolTraceTurn>;
  unattachedTurnsByAnchor: Map<string, ToolTraceTurn[]>;
  leadingUnattachedTurns: ToolTraceTurn[];
}

const callNamePattern = /^[A-Za-z][\w-]*$/;

function isToolCallContent(content: string): boolean {
  const normalized = content.replace(/\r/g, '').trim();
  const newline = normalized.indexOf('\n');
  if (newline <= 0) return false;
  const name = normalized.slice(0, newline).trim();
  const rest = normalized.slice(newline + 1).trim();
  return callNamePattern.test(name) && rest.startsWith('{');
}

function safeDurationMs(startIso: string, endIso: string): number | null {
  const start = Date.parse(startIso);
  const end = Date.parse(endIso);
  if (Number.isNaN(start) || Number.isNaN(end) || end < start) return null;
  return end - start;
}

/** Format a step or turn duration concisely for chips and timeline headers. */
export function formatToolDuration(durationMs: number | null): string | null {
  if (durationMs === null || !Number.isFinite(durationMs) || durationMs < 0) {
    return null;
  }
  if (durationMs < 1000) {
    return `${Math.round(durationMs)} ms`;
  }
  const seconds = durationMs / 1000;
  if (seconds < 60) {
    return `${seconds.toFixed(1)} s`;
  }
  const minutes = Math.floor(seconds / 60);
  const remSeconds = Math.round(seconds % 60);
  return `${minutes} m ${remSeconds} s`;
}

/**
 * Separate tool-call rows from conversational messages and pair each tool
 * invocation with its result, grouped by bot turn.
 */
export function buildToolTrace({
  messages,
  turns = [],
  imagePathForMessage,
}: {
  messages: ToolChatMessage[];
  turns?: ToolChatTurn[];
  imagePathForMessage: (messageId: string) => string;
}): ToolTraceSummary {
  const sorted = [...messages].sort((a, b) => a.position - b.position);
  const activeTurnIds = new Set(
    turns
      .filter((t) => t.status === 'queued' || t.status === 'running')
      .map((t) => t.id),
  );
  const hasAnyActiveTurn = activeTurnIds.size > 0;

  interface MutableTurn {
    turnKey: string;
    botId?: string;
    startedAt: string;
    minPosition: number;
    maxPosition: number;
    lastEventAt: string;
    steps: Array<ToolTraceStep & { awaitedResult?: boolean }>;
    replyMessageId?: string;
    explicitTurnId: boolean;
  }

  const chatMessages: ToolChatMessage[] = [];
  const turnMap = new Map<string, MutableTurn>();
  const turnOrder: MutableTurn[] = [];
  const legacyKeyByBot = new Map<string, string>();
  const unlinkedTurns: MutableTurn[] = [];

  const getOrCreateTurn = (
    turnKey: string,
    msg: ToolChatMessage,
    explicitTurnId: boolean,
  ): MutableTurn => {
    const existing = turnMap.get(turnKey);
    if (existing) {
      existing.maxPosition = Math.max(existing.maxPosition, msg.position);
      existing.lastEventAt = msg.createdAt;
      if (!existing.botId && msg.senderBotId) {
        existing.botId = msg.senderBotId;
      }
      return existing;
    }
    const created: MutableTurn = {
      turnKey,
      botId: msg.senderBotId,
      startedAt: msg.createdAt,
      minPosition: msg.position,
      maxPosition: msg.position,
      lastEventAt: msg.createdAt,
      steps: [],
      explicitTurnId,
    };
    turnMap.set(turnKey, created);
    turnOrder.push(created);
    unlinkedTurns.push(created);
    return created;
  };

  for (const msg of sorted) {
    if (msg.kind !== 'tool') {
      chatMessages.push(msg);

      if (msg.senderType === 'user') {
        legacyKeyByBot.clear();
        continue;
      }

      if (msg.kind === 'text' && msg.senderType === 'bot') {
        const botKey = msg.senderBotId ?? '__bot__';
        legacyKeyByBot.delete(botKey);

        // Attach the most recent preceding unlinked tool turn from the same bot.
        let matchIdx = -1;
        for (let i = unlinkedTurns.length - 1; i >= 0; i -= 1) {
          const candidate = unlinkedTurns[i];
          if (!candidate || candidate.maxPosition >= msg.position) continue;
          if (
            !msg.senderBotId ||
            !candidate.botId ||
            candidate.botId === msg.senderBotId
          ) {
            matchIdx = i;
            break;
          }
        }
        if (matchIdx >= 0) {
          const matched = unlinkedTurns[matchIdx];
          if (matched) {
            matched.replyMessageId = msg.id;
            for (const step of matched.steps) {
              if (step.status === 'running') {
                step.status = 'done';
              }
            }
            unlinkedTurns.splice(matchIdx, 1);
          }
        }
      }
      continue;
    }

    const botKey = msg.senderBotId ?? '__bot__';
    const explicitTurnId = Boolean(msg.tool?.turnId);
    let turnKey = msg.tool?.turnId;
    if (!turnKey) {
      let existingLegacy = legacyKeyByBot.get(botKey);
      if (!existingLegacy) {
        existingLegacy = `legacy-${msg.id}`;
        legacyKeyByBot.set(botKey, existingLegacy);
      }
      turnKey = existingLegacy;
    }

    const turn = getOrCreateTurn(turnKey, msg, explicitTurnId);

    const guardrail = msg.tool?.guardrail;
    if (guardrail) {
      const guardrailKey =
        guardrail.action === 'blocked'
          ? guardrail.direction === 'output'
            ? 'guardrailTraceOutputBlocked'
            : 'guardrailTraceInputBlocked'
          : guardrail.direction === 'output'
            ? 'guardrailTraceOutputRedacted'
            : 'guardrailTraceInputRedacted';
      turn.steps.push({
        id: msg.id,
        turnKey,
        toolCallId: msg.tool?.toolCallId,
        botId: msg.senderBotId,
        callCopy: { key: guardrailKey, image: false },
        status: guardrail.action === 'blocked' ? 'error' : 'done',
        errorCode: msg.tool?.errorCode,
        startedAt: msg.createdAt,
        finishedAt: msg.createdAt,
        durationMs: guardrail.latencyMs,
        guardrail,
        awaitedResult: false,
      });
      continue;
    }

    const phase: 'call' | 'result' =
      msg.tool?.phase ?? (isToolCallContent(msg.content) ? 'call' : 'result');
    const copy: ToolCopy = describeToolMessage('tool', msg.content) ?? {
      key: 'finished',
      image: false,
    };

    const isTurnActive = explicitTurnId
      ? activeTurnIds.has(turnKey)
      : hasAnyActiveTurn;

    if (phase === 'call') {
      turn.steps.push({
        id: msg.id,
        turnKey,
        toolCallId: msg.tool?.toolCallId,
        botId: msg.senderBotId,
        callCopy: copy,
        status: isTurnActive ? 'running' : 'done',
        startedAt: msg.createdAt,
        durationMs: null,
        awaitedResult: true,
      });
      continue;
    }

    const targetCallId = msg.tool?.toolCallId;
    let targetStep: (ToolTraceStep & { awaitedResult?: boolean }) | undefined;
    for (let i = turn.steps.length - 1; i >= 0; i -= 1) {
      const candidate = turn.steps[i];
      if (!candidate || !candidate.awaitedResult) continue;
      if (targetCallId && candidate.toolCallId !== targetCallId) continue;
      targetStep = candidate;
      break;
    }

    const stepError = Boolean(msg.tool?.isError);
    const imagePath = copy.image ? imagePathForMessage(msg.id) : undefined;

    if (targetStep) {
      targetStep.awaitedResult = false;
      targetStep.resultCopy = copy;
      targetStep.status = stepError ? 'error' : 'done';
      targetStep.errorCode = msg.tool?.errorCode;
      targetStep.finishedAt = msg.createdAt;
      targetStep.durationMs = safeDurationMs(
        targetStep.startedAt,
        msg.createdAt,
      );
      if (imagePath) {
        targetStep.imagePath = imagePath;
      }
    } else {
      turn.steps.push({
        id: msg.id,
        turnKey,
        toolCallId: targetCallId,
        botId: msg.senderBotId,
        callCopy: copy,
        status: stepError ? 'error' : 'done',
        errorCode: msg.tool?.errorCode,
        startedAt: msg.createdAt,
        finishedAt: msg.createdAt,
        durationMs: null,
        imagePath,
        awaitedResult: false,
      });
    }
  }

  const turnsByReplyId = new Map<string, ToolTraceTurn>();
  const unattachedTurnsByAnchor = new Map<string, ToolTraceTurn[]>();
  const leadingUnattachedTurns: ToolTraceTurn[] = [];
  const finalTurns: ToolTraceTurn[] = [];

  let totalSteps = 0;
  let runningSteps = 0;
  let errorSteps = 0;

  for (const raw of turnOrder) {
    const turnActive = raw.explicitTurnId
      ? activeTurnIds.has(raw.turnKey)
      : hasAnyActiveTurn && raw === turnOrder[turnOrder.length - 1];

    if (!turnActive) {
      for (const step of raw.steps) {
        if (step.status === 'running') {
          step.status = 'done';
        }
      }
    }

    const hasRunning = raw.steps.some((s) => s.status === 'running');
    const hasError = raw.steps.some((s) => s.status === 'error');
    const turnStatus: ToolTraceTurn['status'] = hasError
      ? 'error'
      : hasRunning
        ? 'running'
        : 'done';

    const spanMs = safeDurationMs(raw.startedAt, raw.lastEventAt);
    const summedStepMs = raw.steps.reduce<number>(
      (acc, s) => acc + (s.durationMs ?? 0),
      0,
    );
    const hasAnyStepDuration = raw.steps.some((s) => s.durationMs !== null);
    const durationMs =
      spanMs !== null && spanMs > 0
        ? spanMs
        : hasAnyStepDuration
          ? summedStepMs
          : null;

    let anchorAfterMessageId: string | undefined;
    if (!raw.replyMessageId) {
      for (let i = chatMessages.length - 1; i >= 0; i -= 1) {
        const candidate = chatMessages[i];
        if (candidate && candidate.position < raw.minPosition) {
          anchorAfterMessageId = candidate.id;
          break;
        }
      }
    }

    const finalized: ToolTraceTurn = {
      turnKey: raw.turnKey,
      botId: raw.botId,
      startedAt: raw.startedAt,
      durationMs,
      status: turnStatus,
      steps: raw.steps,
      replyMessageId: raw.replyMessageId,
      anchorAfterMessageId,
    };

    totalSteps += finalized.steps.length;
    runningSteps += finalized.steps.filter(
      (s) => s.status === 'running',
    ).length;
    errorSteps += finalized.steps.filter((s) => s.status === 'error').length;

    if (finalized.replyMessageId) {
      turnsByReplyId.set(finalized.replyMessageId, finalized);
    } else if (finalized.anchorAfterMessageId) {
      const bucket =
        unattachedTurnsByAnchor.get(finalized.anchorAfterMessageId) ?? [];
      bucket.push(finalized);
      unattachedTurnsByAnchor.set(finalized.anchorAfterMessageId, bucket);
    } else {
      leadingUnattachedTurns.push(finalized);
    }

    finalTurns.push(finalized);
  }

  // Present newest turns first in the sidebar so live work appears at the top.
  finalTurns.reverse();

  return {
    chatMessages,
    turns: finalTurns,
    totalSteps,
    runningSteps,
    errorSteps,
    turnsByReplyId,
    unattachedTurnsByAnchor,
    leadingUnattachedTurns,
  };
}
