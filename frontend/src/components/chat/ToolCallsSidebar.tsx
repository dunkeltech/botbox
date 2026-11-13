'use client';

import {
  AlertTriangle,
  ArrowUp,
  Check,
  ChevronDown,
  ChevronRight,
  Loader2,
} from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import { useTranslations } from 'next-intl';
import { ToolImage } from '@/components/chat/ToolImage';
import {
  formatToolDuration,
  type ToolTraceStep,
  type ToolTraceSummary,
  type ToolTraceTurn,
} from '@/components/chat/toolTrace';
import { BotAvatar, Eyebrow } from '@/components/ui';

type TraceFilter = 'all' | 'running' | 'errors';

function formatTime(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return '';
  return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

function StepCard({
  step,
  defaultExpanded,
}: {
  step: ToolTraceStep;
  defaultExpanded: boolean;
}) {
  const t = useTranslations('chat');
  const [expanded, setExpanded] = useState(
    defaultExpanded || Boolean(step.guardrail),
  );

  useEffect(() => {
    if (defaultExpanded) {
      setExpanded(true);
    }
  }, [defaultExpanded]);

  const callText = step.callCopy.values
    ? t(step.callCopy.key, step.callCopy.values)
    : t(step.callCopy.key);
  const resultText = step.resultCopy
    ? step.resultCopy.values
      ? t(step.resultCopy.key, step.resultCopy.values)
      : t(step.resultCopy.key)
    : null;
  const duration = formatToolDuration(step.durationMs);
  const parameters = step.parameters ?? [];
  const outputTail = step.outputTail;
  const hasDetails = Boolean(
    parameters.length > 0 ||
    outputTail ||
    resultText ||
    step.errorCode ||
    step.imagePath ||
    step.guardrail,
  );

  if (step.guardrail) {
    const guardrail = step.guardrail;
    const isBlocked = guardrail.action === 'blocked';
    const chips = [
      ...guardrail.ruleSlugs,
      ...guardrail.matchLabels.filter(
        (label) => !guardrail.ruleSlugs.includes(label),
      ),
    ];

    const containerClass = isBlocked
      ? 'rounded-sm border-[1.5px] border-dashed border-amber-line bg-amber-tint'
      : 'rounded-sm border border-l-[3px] border-blue-line border-l-blue bg-paper';

    const badgeClass = isBlocked
      ? 'border-amber-line bg-paper text-amber'
      : 'border-blue-line bg-blue-tint text-blue';

    const chipClass = isBlocked
      ? 'border-amber-line bg-paper text-amber'
      : 'border-blue-line bg-blue-tint text-blue';

    const footerClass = isBlocked
      ? 'border-t border-amber-line bg-paper/70 px-3 py-1.5 font-mono text-[11px] text-ink-2'
      : 'border-t border-line bg-paper-2 px-3 py-1.5 font-mono text-[11px] text-muted';

    return (
      <div
        className={containerClass}
        data-guardrail-trace-action={guardrail.action}
      >
        <button
          type="button"
          aria-expanded={expanded}
          onClick={() => setExpanded((prev) => !prev)}
          className="flex min-h-[44px] w-full items-start justify-between gap-2 px-3 py-2.5 text-left focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
        >
          <div className="flex min-w-0 flex-1 flex-wrap items-center gap-2">
            {isBlocked ? (
              <AlertTriangle
                className="h-3.5 w-3.5 shrink-0 text-amber"
                aria-hidden="true"
              />
            ) : (
              <svg
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                strokeWidth="2"
                strokeLinecap="round"
                strokeLinejoin="round"
                className="h-3.5 w-3.5 shrink-0 text-blue"
                aria-hidden="true"
              >
                <path d="M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z" />
              </svg>
            )}
            <span
              className={`text-[13px] font-bold ${isBlocked ? 'text-amber' : 'text-ink'}`}
            >
              {callText}
            </span>
            <span
              className={`inline-flex items-center rounded-pill border px-2 py-0.5 text-[11px] font-bold ${badgeClass}`}
            >
              {isBlocked
                ? t('guardrailBlockedBadge')
                : t('guardrailRedactedBadge')}
            </span>
          </div>
          <span className="shrink-0 font-mono text-[11px] tabular-nums text-muted">
            {`${guardrail.latencyMs} ms`}
          </span>
        </button>

        {expanded ? (
          <>
            <div className="flex flex-col gap-2 px-3 pb-2.5">
              {chips.length > 0 ? (
                <div className="flex flex-wrap gap-1.5">
                  {chips.map((chip) => (
                    <span
                      key={chip}
                      className={`inline-flex items-center rounded-xs border px-2 py-0.5 font-mono text-[11px] font-bold ${chipClass}`}
                    >
                      {chip}
                    </span>
                  ))}
                </div>
              ) : null}
              <p className="text-[12px] leading-[1.45] text-ink-2">
                {guardrail.summary}
              </p>
            </div>
            <div className={footerClass}>
              {t('guardrailTraceFooter', {
                route: guardrail.routePath || '-',
                model: guardrail.model || guardrail.provider,
                action: guardrail.action,
              })}
            </div>
          </>
        ) : null}
      </div>
    );
  }

  const borderTone =
    step.status === 'error'
      ? 'border-dashed border-amber-line bg-paper'
      : step.status === 'running'
        ? 'border-blue-line bg-paper'
        : 'border-line bg-paper';

  const statusBadge =
    step.status === 'error' ? (
      <span className="inline-flex items-center gap-1 rounded-pill border border-amber-line bg-amber-tint px-2 py-0.5 text-[11px] font-bold text-amber">
        <AlertTriangle className="h-3 w-3 shrink-0" aria-hidden="true" />
        <span>{t('toolStatusError')}</span>
      </span>
    ) : step.status === 'running' ? (
      <span className="inline-flex items-center gap-1 rounded-pill border border-blue-line bg-blue-tint px-2 py-0.5 text-[11px] font-bold text-blue">
        <Loader2 className="h-3 w-3 shrink-0 animate-spin" aria-hidden="true" />
        <span>{t('toolStatusRunning')}</span>
      </span>
    ) : (
      <span className="inline-flex items-center gap-1 rounded-pill border border-mint-line bg-mint-soft px-2 py-0.5 text-[11px] font-bold text-green-600">
        <Check className="h-3 w-3 shrink-0" aria-hidden="true" />
        <span>{t('toolStatusDone')}</span>
      </span>
    );

  return (
    <div className={`rounded-sm border ${borderTone}`}>
      {hasDetails ? (
        <button
          type="button"
          aria-expanded={expanded}
          aria-label={expanded ? t('toolCollapseStep') : t('toolExpandStep')}
          onClick={() => setExpanded((prev) => !prev)}
          className="flex min-h-[44px] w-full items-center justify-between gap-2 px-3 py-2 text-left focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
        >
          <div className="flex min-w-0 flex-1 flex-wrap items-center gap-2">
            {statusBadge}
            <span className="min-w-0 flex-1 truncate font-mono text-[12px] font-bold text-ink">
              {callText}
            </span>
          </div>
          <div className="flex shrink-0 items-center gap-1.5">
            {duration ? (
              <span className="font-mono text-[11px] text-muted">
                {duration}
              </span>
            ) : null}
            {expanded ? (
              <ChevronDown className="h-4 w-4 text-muted" aria-hidden="true" />
            ) : (
              <ChevronRight className="h-4 w-4 text-muted" aria-hidden="true" />
            )}
          </div>
        </button>
      ) : (
        <div className="flex min-h-[40px] items-center justify-between gap-2 px-3 py-2">
          <div className="flex min-w-0 flex-1 flex-wrap items-center gap-2">
            {statusBadge}
            <span className="min-w-0 flex-1 truncate font-mono text-[12px] font-bold text-ink">
              {callText}
            </span>
          </div>
          {duration ? (
            <span className="shrink-0 font-mono text-[11px] text-muted">
              {duration}
            </span>
          ) : null}
        </div>
      )}

      {expanded && hasDetails ? (
        <div className="flex flex-col gap-2 border-t border-line px-3 py-2.5">
          {parameters.length > 0 ? (
            <div className="rounded-sm border border-line bg-paper-2 px-2.5 py-2">
              <div className="mb-1 text-[10px] font-bold uppercase tracking-[0.06em] text-muted">
                {t('toolParametersLabel')}
              </div>
              <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-2.5 gap-y-1 font-mono text-[11px] leading-[1.45]">
                {parameters.map((param) => (
                  <div key={param.key} className="contents">
                    <dt className="truncate font-bold text-muted">
                      {`${param.key}:`}
                    </dt>
                    <dd className="min-w-0 whitespace-pre-wrap break-words text-ink">
                      {param.value}
                    </dd>
                  </div>
                ))}
              </dl>
            </div>
          ) : null}

          {outputTail ? (
            <div className="rounded-sm bg-terminal px-2.5 py-2 text-on-dark">
              <div className="mb-1.5 flex flex-wrap items-center justify-between gap-2 text-[10px] font-bold uppercase tracking-[0.06em] text-on-dark-muted">
                <span>
                  {outputTail.hiddenLines > 0
                    ? t('toolOutputLastLines', {
                        count: outputTail.lines.length,
                      })
                    : t('toolOutputLabel')}
                </span>
                {outputTail.hiddenLines > 0 ? (
                  <span className="font-mono font-normal normal-case tracking-normal text-on-dark-muted">
                    {t('toolOutputHiddenLines', {
                      count: outputTail.hiddenLines,
                    })}
                  </span>
                ) : null}
              </div>
              <pre
                className={`overflow-x-auto whitespace-pre-wrap break-words font-mono text-[11px] leading-[1.5] ${
                  step.status === 'error'
                    ? 'text-amber-on-dark'
                    : 'text-on-dark'
                }`}
              >
                {outputTail.lines.join('\n')}
              </pre>
            </div>
          ) : resultText ? (
            <div className="flex flex-col gap-0.5">
              <span className="text-[11px] font-bold uppercase tracking-[0.06em] text-muted">
                {t('toolResultLabel')}
              </span>
              <p className="font-mono text-[12px] text-ink-2">{resultText}</p>
            </div>
          ) : null}

          {step.errorCode ? (
            <p className="rounded-xs border border-amber-line bg-amber-tint px-2 py-1 font-mono text-[11px] font-bold text-amber">
              {t('toolErrorCode', { code: step.errorCode })}
            </p>
          ) : null}
          {step.imagePath ? (
            <ToolImage
              path={step.imagePath}
              alt={t('toolImageAlt')}
              className="max-h-48 w-full rounded-sm border border-line object-contain bg-paper-2"
            />
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

function TurnSection({
  turn,
  selected,
  resolveBot,
  onJumpToMessage,
  registerRef,
}: {
  turn: ToolTraceTurn;
  selected: boolean;
  resolveBot?: (botId?: string) => { name: string; avatarSeed: string } | null;
  onJumpToMessage?: (messageId: string) => void;
  registerRef: (turnKey: string, el: HTMLElement | null) => void;
}) {
  const t = useTranslations('chat');
  const timeLabel = formatTime(turn.startedAt);
  const duration = formatToolDuration(turn.durationMs);
  const botInfo = resolveBot ? resolveBot(turn.botId) : null;

  return (
    <section
      ref={(el) => registerRef(turn.turnKey, el)}
      data-turn-key={turn.turnKey}
      className={`flex flex-col gap-2 rounded-md border p-2.5 ${
        selected ? 'border-green bg-mint-soft/45' : 'border-line bg-paper-2/60'
      }`}
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex min-w-0 items-center gap-2">
          {botInfo ? (
            <BotAvatar seed={botInfo.avatarSeed} size={20} decorative />
          ) : null}
          <span className="text-[11px] font-bold uppercase tracking-[0.06em] text-muted">
            {botInfo ? `${botInfo.name} · ` : ''}
            {turn.status === 'running'
              ? t('toolTurnLive')
              : t('toolTurnLabel', { time: timeLabel })}
          </span>
          {duration ? (
            <span className="font-mono text-[11px] text-muted">{duration}</span>
          ) : null}
        </div>
        {turn.replyMessageId && onJumpToMessage ? (
          <button
            type="button"
            onClick={() => onJumpToMessage(turn.replyMessageId!)}
            className="inline-flex min-h-[28px] items-center gap-1 rounded-xs px-1.5 text-[11px] font-bold text-green-600 hover:bg-mint-soft focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
          >
            <span>{t('toolJumpToMessage')}</span>
            <ArrowUp className="h-3 w-3" aria-hidden="true" />
          </button>
        ) : null}
      </div>

      <div className="flex flex-col gap-2">
        {turn.steps.map((step) => (
          <StepCard key={step.id} step={step} defaultExpanded={false} />
        ))}
      </div>
    </section>
  );
}

/**
 * Dedicated Tool Calls sidebar panel showing paired invocation + outcome cards
 * grouped by bot turn, with filtering, auto-follow, and jump-to-reply links.
 */
export function ToolCallsSidebar({
  trace,
  selectedTurnKey,
  resolveBot,
  onJumpToMessage,
}: {
  trace: ToolTraceSummary;
  selectedTurnKey?: string | null;
  resolveBot?: (botId?: string) => { name: string; avatarSeed: string } | null;
  onJumpToMessage?: (messageId: string) => void;
}) {
  const t = useTranslations('chat');
  const [filter, setFilter] = useState<TraceFilter>('all');
  const [autoFollow, setAutoFollow] = useState(true);
  const turnRefs = useRef<Map<string, HTMLElement>>(new Map());

  const registerRef = (turnKey: string, el: HTMLElement | null) => {
    if (el) {
      turnRefs.current.set(turnKey, el);
    } else {
      turnRefs.current.delete(turnKey);
    }
  };

  useEffect(() => {
    if (!selectedTurnKey) return;
    const node = turnRefs.current.get(selectedTurnKey);
    if (node && typeof node.scrollIntoView === 'function') {
      node.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
    }
  }, [selectedTurnKey]);

  const newestTurnKey = trace.turns[0]?.turnKey;
  useEffect(() => {
    if (!autoFollow || !newestTurnKey || trace.runningSteps === 0) return;
    const node = turnRefs.current.get(newestTurnKey);
    if (node && typeof node.scrollIntoView === 'function') {
      node.scrollIntoView({ block: 'nearest' });
    }
  }, [autoFollow, newestTurnKey, trace.runningSteps, trace.totalSteps]);

  const filteredTurns = trace.turns
    .map((turn) => {
      if (filter === 'all') return turn;
      const steps = turn.steps.filter((step) =>
        filter === 'running'
          ? step.status === 'running'
          : step.status === 'error',
      );
      if (steps.length === 0) return null;
      return { ...turn, steps };
    })
    .filter((turn): turn is ToolTraceTurn => turn !== null);

  return (
    <div className="flex flex-col gap-3" aria-label={t('toolSidebarLabel')}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <Eyebrow>{t('toolTraceEyebrow')}</Eyebrow>
        <button
          type="button"
          aria-pressed={autoFollow}
          onClick={() => setAutoFollow((prev) => !prev)}
          className={`inline-flex min-h-[28px] items-center gap-1.5 rounded-pill border px-2.5 text-[11px] font-bold transition duration-150 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green ${
            autoFollow
              ? 'border-mint-line bg-mint-soft text-green-600'
              : 'border-line bg-paper text-muted'
          }`}
        >
          <span
            aria-hidden="true"
            className={`h-2 w-2 rounded-full ${
              autoFollow ? 'bg-green' : 'bg-line-strong'
            }`}
          />
          <span>{t('toolAutoFollow')}</span>
        </button>
      </div>

      <div
        role="group"
        aria-label={t('toolSidebarLabel')}
        className="flex flex-wrap gap-1.5"
      >
        <button
          type="button"
          aria-pressed={filter === 'all'}
          onClick={() => setFilter('all')}
          className={`min-h-[32px] rounded-pill border px-2.5 text-[12px] font-bold focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green ${
            filter === 'all'
              ? 'border-mint-line bg-mint-soft text-green-600'
              : 'border-line bg-paper text-ink-2 hover:bg-paper-2'
          }`}
        >
          {t('toolFilterAll', { count: trace.totalSteps })}
        </button>
        <button
          type="button"
          aria-pressed={filter === 'running'}
          onClick={() => setFilter('running')}
          className={`min-h-[32px] rounded-pill border px-2.5 text-[12px] font-bold focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green ${
            filter === 'running'
              ? 'border-blue-line bg-blue-tint text-blue'
              : 'border-line bg-paper text-ink-2 hover:bg-paper-2'
          }`}
        >
          {t('toolFilterRunning', { count: trace.runningSteps })}
        </button>
        <button
          type="button"
          aria-pressed={filter === 'errors'}
          onClick={() => setFilter('errors')}
          className={`min-h-[32px] rounded-pill border px-2.5 text-[12px] font-bold focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green ${
            filter === 'errors'
              ? 'border-amber-line bg-amber-tint text-amber'
              : 'border-line bg-paper text-ink-2 hover:bg-paper-2'
          }`}
        >
          {t('toolFilterErrors', { count: trace.errorSteps })}
        </button>
      </div>

      {trace.turns.length === 0 ? (
        <p className="rounded-sm border border-line bg-paper px-3 py-4 text-[13px] text-muted">
          {t('toolEmpty')}
        </p>
      ) : filteredTurns.length === 0 ? (
        <p className="rounded-sm border border-line bg-paper px-3 py-4 text-[13px] text-muted">
          {t('toolEmptyFiltered')}
        </p>
      ) : (
        <div className="flex flex-col gap-3">
          {filteredTurns.map((turn) => (
            <TurnSection
              key={turn.turnKey}
              turn={turn}
              selected={selectedTurnKey === turn.turnKey}
              resolveBot={resolveBot}
              onJumpToMessage={onJumpToMessage}
              registerRef={registerRef}
            />
          ))}
        </div>
      )}
    </div>
  );
}
