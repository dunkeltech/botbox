'use client';

import type { ReactNode } from 'react';
import { useTranslations } from 'next-intl';

import { ChatTurnToolChip } from '@/components/chat/ChatTurnToolChip';
import { GuardrailNoticeCard } from '@/components/chat/GuardrailNoticeCard';
import { MemorySavedCard } from '@/components/chat/MemorySavedCard';
import { SecretRequestCard } from '@/components/chat/SecretRequestCard';
import type {
  ToolChatMessage,
  ToolTraceTurn,
} from '@/components/chat/toolTrace';
import type { useSecretRequestControl } from '@/components/chat/useSecretRequestControl';
import { BotAvatar } from '@/components/ui';
import type { WorkspaceRole } from '@/types/api';

type SecretRequestActions = Pick<
  ReturnType<typeof useSecretRequestControl>,
  'busyRequestId' | 'requestError' | 'openModal' | 'openTakeover' | 'deny'
>;

/** Renders common transcript cards and keeps tool-only turns beside their anchor. */
export function ChatTranscriptRow({
  message,
  workspaceId,
  role,
  cardClassName,
  secretControl,
  memoryBotId,
  canManageMemory,
  refreshHistory,
  onInspectTurn,
  toolTurnsAfter,
  renderMessage,
}: {
  message: ToolChatMessage;
  workspaceId: string;
  role: WorkspaceRole;
  cardClassName?: string;
  secretControl: SecretRequestActions;
  memoryBotId?: string;
  canManageMemory: (botId: string) => boolean;
  refreshHistory: () => Promise<void>;
  onInspectTurn?: (turnKey: string) => void;
  toolTurnsAfter: ReactNode;
  renderMessage: () => ReactNode;
}) {
  let content: ReactNode;
  if (message.kind === 'secret_request' && message.secretRequest) {
    const summary = message.secretRequest;
    const requestId = summary.secretRequestId;
    content = (
      <SecretRequestCard
        className={cardClassName}
        summary={summary}
        role={role}
        busy={secretControl.busyRequestId === requestId}
        errorKey={
          secretControl.requestError?.id === requestId
            ? secretControl.requestError.key
            : null
        }
        onProvide={secretControl.openModal}
        onTakeover={secretControl.openTakeover}
        onDeny={(request) => void secretControl.deny(request)}
      />
    );
  } else if (message.kind === 'guardrail' && message.guardrail) {
    content = (
      <GuardrailNoticeCard
        className={cardClassName}
        guardrail={message.guardrail}
        role={role}
        onInspect={onInspectTurn}
      />
    );
  } else if (message.kind === 'memory' && message.memory && memoryBotId) {
    content = (
      <MemorySavedCard
        className={cardClassName}
        workspaceId={workspaceId}
        botId={memoryBotId}
        memory={message.memory}
        canManage={canManageMemory(memoryBotId)}
        onUpdated={refreshHistory}
      />
    );
  } else {
    content = renderMessage();
  }

  return (
    <div className="flex flex-col gap-3">
      {content}
      {toolTurnsAfter}
    </div>
  );
}

/** Displays a tool turn that has no bot reply of its own. */
export function UnattachedToolTurnRow({
  turn,
  name,
  avatarSeed,
  layout,
  selected,
  onInspect,
}: {
  turn: ToolTraceTurn;
  name: string;
  avatarSeed: string;
  layout: 'session' | 'slack' | 'group';
  selected: boolean;
  onInspect?: (turnKey: string) => void;
}) {
  const t = useTranslations('team');
  const isGroup = layout === 'group';
  const Header = isGroup ? 'p' : 'div';

  return (
    <div
      className={isGroup ? 'flex gap-3 px-5 py-1' : 'flex items-start gap-2.5'}
    >
      <BotAvatar
        seed={avatarSeed}
        mood={
          isGroup ? undefined : turn.status === 'running' ? 'thinking' : 'idle'
        }
        size={layout === 'session' ? 28 : 36}
        decorative
      />
      <div className="min-w-0 flex-1">
        <Header className="flex flex-wrap items-baseline gap-2">
          <span
            className={
              isGroup
                ? 'text-[15px] font-black text-ink'
                : 'text-[14px] font-black text-ink'
            }
          >
            {name}
          </span>
          {layout !== 'session' ? (
            <span className="rounded-xs bg-mint-soft px-1.5 py-0.5 text-[11px] font-black uppercase tracking-[0.06em] text-green-600">
              {t('botBadge')}
            </span>
          ) : null}
        </Header>
        <ChatTurnToolChip
          turn={turn}
          selected={selected}
          onInspect={(turnKey) => onInspect?.(turnKey)}
        />
      </div>
    </div>
  );
}
