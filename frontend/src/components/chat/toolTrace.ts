// SPDX-License-Identifier: AGPL-3.0-only

import {
  describeToolMessage,
  type ToolCopy,
} from '@/components/chat/toolMessage';
import type { components } from '@/types/openapi.gen';

export type ToolChatMessage = components['schemas']['ChatMessage'];
export type ToolChatTurn = components['schemas']['ChatTurn'];

export interface ToolTraceParameter {
  key: string;
  value: string;
}

export interface ToolTraceOutputTail {
  lines: string[];
  hiddenLines: number;
  totalLines: number;
}

export interface ToolTraceStep {
  id: string;
  turnKey: string;
  toolCallId?: string;
  botId?: string;
  callCopy: ToolCopy;
  resultCopy?: ToolCopy;
  parameters?: ToolTraceParameter[];
  outputTail?: ToolTraceOutputTail;
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
const imageMarkerPattern = /\[Image:\s*source:[^\]]*\]/gi;
const maxParameterValueLength = 400;
const maxOutputLineLength = 1000;
const defaultOutputTailLines = 5;

function stripImageMarkers(content: string): string {
  return content.replace(imageMarkerPattern, '').replace(/\r\n?/g, '\n');
}

function truncateText(text: string, maxLength: number): string {
  if (text.length <= maxLength) return text;
  return `${text.slice(0, maxLength - 1)}…`;
}

function formatParameterValue(value: unknown): string {
  if (typeof value === 'string') {
    return truncateText(
      stripImageMarkers(value).trim(),
      maxParameterValueLength,
    );
  }
  if (typeof value === 'number' || typeof value === 'boolean') {
    return String(value);
  }
  if (value === null) {
    return 'null';
  }
  try {
    return truncateText(
      stripImageMarkers(JSON.stringify(value)),
      maxParameterValueLength,
    );
  } catch {
    return '';
  }
}

/**
 * Extract structured key/value parameters from a stored tool-call message
 * (`<ToolName>\n<JSON>`), returning an empty list for empty or non-object args.
 */
export function extractToolParameters(content: string): ToolTraceParameter[] {
  const normalized = stripImageMarkers(content).trim();
  const newline = normalized.indexOf('\n');
  if (newline <= 0) return [];
  const name = normalized.slice(0, newline).trim();
  const rest = normalized.slice(newline + 1).trim();
  if (!callNamePattern.test(name) || !rest.startsWith('{')) {
    return [];
  }
  let parsed: unknown;
  try {
    parsed = JSON.parse(rest);
  } catch {
    return [];
  }
  if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
    return [];
  }
  const entries = Object.entries(parsed as Record<string, unknown>);
  if (entries.length === 0) return [];
  return entries.map(([key, value]) => ({
    key,
    value: formatParameterValue(value),
  }));
}

/**
 * Extract the last `maxLines` lines of a stored tool result after stripping
 * image source markers so raw sandbox image paths are never exposed in text.
 */
export function extractToolOutputTail(
  content: string,
  maxLines = defaultOutputTailLines,
): ToolTraceOutputTail | undefined {
  const cleaned = stripImageMarkers(content);
  const rawLines = cleaned.split('\n');
  let start = 0;
  let end = rawLines.length;
  while (start < end && rawLines[start]?.trim() === '') {
    start += 1;
  }
  while (end > start && rawLines[end - 1]?.trim() === '') {
    end -= 1;
  }
  const nonEmptyBounded = rawLines.slice(start, end);
  if (nonEmptyBounded.length === 0) {
    return undefined;
  }
  const totalLines = nonEmptyBounded.length;
  const tailCount = Math.max(1, maxLines);
  const lines = nonEmptyBounded
    .slice(-tailCount)
    .map((line) => truncateText(line, maxOutputLineLength));
  return {
    lines,
    hiddenLines: Math.max(0, totalLines - lines.length),
    totalLines,
  };
}

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

type MutableStep = ToolTraceStep & { awaitedResult?: boolean };

interface MutableTurn {
  turnKey: string;
  botId?: string;
  startedAt: string;
  minPosition: number;
  maxPosition: number;
  lastEventAt: string;
  steps: MutableStep[];
  replyMessageId?: string;
  explicitTurnId: boolean;
}

function attachBotReply(
  unlinkedTurns: MutableTurn[],
  message: ToolChatMessage,
) {
  // Match the latest preceding tool turn from the same bot. Older history may omit the bot ID.
  for (let index = unlinkedTurns.length - 1; index >= 0; index -= 1) {
    const turn = unlinkedTurns[index];
    if (!turn || turn.maxPosition >= message.position) continue;
    if (message.senderBotId && turn.botId && turn.botId !== message.senderBotId)
      continue;

    turn.replyMessageId = message.id;
    for (const step of turn.steps) {
      if (step.status === 'running') step.status = 'done';
    }
    unlinkedTurns.splice(index, 1);
    return;
  }
}

function recordToolMessage(
  turn: MutableTurn,
  msg: ToolChatMessage,
  isTurnActive: boolean,
  imagePathForMessage: (messageId: string) => string,
) {
  const { turnKey } = turn;
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
    return;
  }

  const phase: 'call' | 'result' =
    msg.tool?.phase ?? (isToolCallContent(msg.content) ? 'call' : 'result');
  const copy: ToolCopy = describeToolMessage('tool', msg.content) ?? {
    key: 'finished',
    image: false,
  };

  if (phase === 'call') {
    const parameters = extractToolParameters(msg.content);
    turn.steps.push({
      id: msg.id,
      turnKey,
      toolCallId: msg.tool?.toolCallId,
      botId: msg.senderBotId,
      callCopy: copy,
      ...(parameters.length > 0 ? { parameters } : {}),
      status: isTurnActive ? 'running' : 'done',
      startedAt: msg.createdAt,
      durationMs: null,
      awaitedResult: true,
    });
    return;
  }

  const targetCallId = msg.tool?.toolCallId;
  let targetStep: MutableStep | undefined;
  for (let i = turn.steps.length - 1; i >= 0; i -= 1) {
    const candidate = turn.steps[i];
    if (!candidate || !candidate.awaitedResult) continue;
    if (targetCallId && candidate.toolCallId !== targetCallId) continue;
    targetStep = candidate;
    break;
  }

  const stepError = Boolean(msg.tool?.isError);
  const imagePath = copy.image ? imagePathForMessage(msg.id) : undefined;
  const outputTail = extractToolOutputTail(msg.content);

  if (targetStep) {
    targetStep.awaitedResult = false;
    targetStep.resultCopy = copy;
    if (outputTail) {
      targetStep.outputTail = outputTail;
    }
    targetStep.status = stepError ? 'error' : 'done';
    targetStep.errorCode = msg.tool?.errorCode;
    targetStep.finishedAt = msg.createdAt;
    targetStep.durationMs = safeDurationMs(targetStep.startedAt, msg.createdAt);
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
      ...(outputTail ? { outputTail } : {}),
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

function collectToolTurns(
  messages: ToolChatMessage[],
  activeTurnIds: ReadonlySet<string>,
  imagePathForMessage: (messageId: string) => string,
) {
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

  for (const msg of messages) {
    if (msg.kind !== 'tool') {
      chatMessages.push(msg);

      if (msg.senderType === 'user') {
        legacyKeyByBot.clear();
        continue;
      }

      if (msg.kind === 'text' && msg.senderType === 'bot') {
        legacyKeyByBot.delete(msg.senderBotId ?? '__bot__');
        attachBotReply(unlinkedTurns, msg);
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

    const isTurnActive = explicitTurnId
      ? activeTurnIds.has(turnKey)
      : activeTurnIds.size > 0;
    recordToolMessage(turn, msg, isTurnActive, imagePathForMessage);
  }

  return { chatMessages, turnOrder };
}

function finalizeToolTurn(
  raw: MutableTurn,
  turnActive: boolean,
  chatMessages: ToolChatMessage[],
): ToolTraceTurn {
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

  return {
    turnKey: raw.turnKey,
    botId: raw.botId,
    startedAt: raw.startedAt,
    durationMs,
    status: turnStatus,
    steps: raw.steps,
    replyMessageId: raw.replyMessageId,
    anchorAfterMessageId,
  };
}

function summarizeToolTurns(
  chatMessages: ToolChatMessage[],
  turnOrder: MutableTurn[],
  activeTurnIds: ReadonlySet<string>,
): ToolTraceSummary {
  const turnsByReplyId = new Map<string, ToolTraceTurn>();
  const unattachedTurnsByAnchor = new Map<string, ToolTraceTurn[]>();
  const leadingUnattachedTurns: ToolTraceTurn[] = [];
  const finalTurns: ToolTraceTurn[] = [];

  let totalSteps = 0;
  let runningSteps = 0;
  let errorSteps = 0;

  const lastTurn = turnOrder[turnOrder.length - 1];
  for (const raw of turnOrder) {
    const turnActive = raw.explicitTurnId
      ? activeTurnIds.has(raw.turnKey)
      : activeTurnIds.size > 0 && raw === lastTurn;

    const finalized = finalizeToolTurn(raw, turnActive, chatMessages);

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
  const activeTurnIds = new Set(
    turns
      .filter((t) => t.status === 'queued' || t.status === 'running')
      .map((t) => t.id),
  );
  const sorted = [...messages].sort((a, b) => a.position - b.position);
  const { chatMessages, turnOrder } = collectToolTurns(
    sorted,
    activeTurnIds,
    imagePathForMessage,
  );
  return summarizeToolTurns(chatMessages, turnOrder, activeTurnIds);
}
