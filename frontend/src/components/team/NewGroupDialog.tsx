'use client';

import { useState, type FormEvent } from 'react';
import { useTranslations } from 'next-intl';
import {
  Alert,
  BotAvatar,
  Button,
  Input,
  ModalDialog,
  Textarea,
} from '@/components/ui';
import type { Bot, WorkspaceMember, WorkspaceRole } from '@/types/api';
import type { components } from '@/types/openapi.gen';
import { initials } from '@/components/team/text';
import { TalkSwitch } from '@/components/team/TalkSwitch';

type CreateGroup = components['schemas']['CreateGroupRequest'];

function statusKey(
  status: Bot['status'],
): 'statusWorking' | 'statusThinking' | 'statusIdle' | 'statusStopped' {
  if (status === 'action') return 'statusWorking';
  if (status === 'thinking') return 'statusThinking';
  if (status === 'stopped') return 'statusStopped';
  return 'statusIdle';
}

/**
 * New group dialog. The caller stays a member because the API adds them
 * even when the body omits them, and the lead has to be a selected bot.
 */
export function NewGroupDialog({
  userId,
  displayName,
  bots,
  humans,
  initialBotId,
  creating,
  error,
  onClose,
  onCreate,
}: {
  userId: string;
  displayName: string;
  bots: Bot[];
  humans: WorkspaceMember[];
  initialBotId?: string;
  creating: boolean;
  error: string | null;
  onClose: () => void;
  onCreate: (body: CreateGroup) => Promise<void>;
}) {
  const t = useTranslations('team');
  const actions = useTranslations('actions');
  const roles = useTranslations('roles');
  const [name, setName] = useState('');
  const [goal, setGoal] = useState('');
  const [botIds, setBotIds] = useState<string[]>(
    initialBotId ? [initialBotId] : [],
  );
  const [humanIds, setHumanIds] = useState<string[]>([userId]);
  const [leadId, setLeadId] = useState(initialBotId ?? '');
  const [botTalk, setBotTalk] = useState(true);
  const [turnLimit, setTurnLimit] = useState(20);
  const people = humans.some((human) => human.userId === userId)
    ? humans
    : [
        {
          workspaceId: '',
          userId,
          email: '',
          displayName,
          locale: 'en',
          role: 'member' as WorkspaceRole,
          joinedAt: '',
        },
        ...humans,
      ];
  const selectedBots = bots.filter((bot) => botIds.includes(bot.id));
  const ready =
    name.trim() !== '' && selectedBots.length > 0 && botIds.includes(leadId);

  function toggleBot(id: string) {
    setBotIds((current) => {
      const next = current.includes(id)
        ? current.filter((row) => row !== id)
        : [...current, id];
      setLeadId((lead) => (next.includes(lead) ? lead : (next[0] ?? '')));
      return next;
    });
  }

  function toggleHuman(id: string) {
    if (id === userId) return;
    setHumanIds((current) =>
      current.includes(id)
        ? current.filter((row) => row !== id)
        : [...current, id],
    );
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!ready || creating) return;
    await onCreate({
      id: crypto.randomUUID(),
      name: name.trim(),
      goal: goal.trim(),
      leadBotId: leadId,
      botTalk,
      turnLimit,
      answerPolicy: 'mentioned_else_lead',
      askBefore: [],
      budgetLimitMicros: null,
      members: [
        { type: 'user', id: userId, roleText: '' },
        ...humanIds
          .filter((id) => id !== userId)
          .map((id) => ({ type: 'user' as const, id, roleText: '' })),
        ...botIds.map((id) => ({ type: 'bot' as const, id, roleText: '' })),
      ],
    });
  }

  return (
    <ModalDialog
      open
      onClose={onClose}
      title={t('dialogTitle')}
      subtitle={t('dialogLead')}
      closeLabel={actions('close')}
    >
      <form
        onSubmit={(event) => void submit(event)}
        className="flex max-h-[70vh] flex-col gap-4 overflow-y-auto"
      >
        <Input
          label={t('name')}
          value={name}
          maxLength={100}
          placeholder={t('namePlaceholder')}
          onChange={(event) => setName(event.target.value)}
        />
        <Textarea
          label={t('goal')}
          value={goal}
          maxLength={8192}
          placeholder={t('goalPlaceholder')}
          onChange={(event) => setGoal(event.target.value)}
        />
        <fieldset className="flex flex-col gap-2">
          <legend className="text-[14px] font-bold">
            {t('memberLegend')}{' '}
            <span className="font-normal text-muted">
              {t('memberCount', {
                bots: selectedBots.length,
                humans: humanIds.length,
              })}
            </span>
          </legend>
          <div className="grid gap-2">
            {bots.map((bot) => {
              const pressed = botIds.includes(bot.id);
              return (
                <button
                  key={bot.id}
                  type="button"
                  aria-pressed={pressed}
                  onClick={() => toggleBot(bot.id)}
                  className={`flex min-h-11 items-center gap-2 rounded-sm border-[1.5px] px-2.5 py-1.5 text-left focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green ${pressed ? 'border-mint-line bg-mint-soft' : 'border-line bg-paper'}`}
                >
                  <BotAvatar
                    seed={bot.avatarSeed}
                    mood={bot.mood}
                    size={36}
                    showStatusDot
                    decorative
                  />
                  <span className="min-w-0 flex-1">
                    <span className="block font-black">{bot.name}</span>
                    <span className="block truncate text-[12px] text-muted">
                      {t(statusKey(bot.status))}
                      {bot.description ? ` · ${bot.description}` : ''}
                    </span>
                  </span>
                </button>
              );
            })}
          </div>
          <div className="grid gap-2">
            {people.map((human) => {
              const pressed = humanIds.includes(human.userId);
              return (
                <button
                  key={human.userId}
                  type="button"
                  aria-pressed={pressed}
                  onClick={() => toggleHuman(human.userId)}
                  className={`flex min-h-11 items-center gap-2 rounded-sm border-[1.5px] px-2.5 py-1.5 text-left focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green ${pressed ? 'border-mint-line bg-mint-soft' : 'border-line bg-paper'}`}
                >
                  <span className="grid h-9 w-9 shrink-0 place-items-center rounded-sm bg-hex-3 text-[12px] font-black text-paper">
                    {initials(human.displayName)}
                  </span>
                  <span className="min-w-0 flex-1">
                    <span className="block font-black">
                      {human.displayName}
                    </span>
                    <span className="block text-[12px] text-muted">
                      {t('humanBadge')} · {roles(human.role)}
                    </span>
                  </span>
                </button>
              );
            })}
          </div>
          {selectedBots.length === 0 ? (
            <p className="text-[14px] text-ink-2">{t('needBot')}</p>
          ) : null}
        </fieldset>
        {selectedBots.length > 0 ? (
          <div className="flex flex-col gap-2">
            <span className="text-[14px] font-bold">
              {t('lead')}{' '}
              <span className="font-normal text-muted">{t('leadHint')}</span>
            </span>
            <div
              className="flex flex-wrap gap-1.5"
              role="group"
              aria-label={t('lead')}
            >
              {selectedBots.map((bot) => {
                const pressed = leadId === bot.id;
                return (
                  <button
                    key={bot.id}
                    type="button"
                    aria-pressed={pressed}
                    onClick={() => setLeadId(bot.id)}
                    className={`inline-flex min-h-11 items-center rounded-pill border-[1.5px] px-3.5 text-[14px] font-bold focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green ${pressed ? 'border-green-600 bg-green-600 text-paper' : 'border-line bg-paper text-ink'}`}
                  >
                    {bot.name}
                  </button>
                );
              })}
            </div>
          </div>
        ) : null}
        <div className="flex flex-col gap-3 rounded-sm border border-line bg-paper-2 p-3">
          <TalkSwitch
            checked={botTalk}
            label={t('talkSwitch')}
            hint={t('talkHint')}
            onToggle={() => setBotTalk((value) => !value)}
          />
          <label className="flex flex-wrap items-center gap-2 text-[14px] font-bold">
            {t('checkIn')}
            <select
              value={String(turnLimit)}
              onChange={(event) => setTurnLimit(Number(event.target.value))}
              className="min-h-11 rounded-sm border-[1.5px] border-line-strong bg-paper px-2 text-[14px] font-normal focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
            >
              {[10, 20, 50].map((count) => (
                <option key={count} value={count}>
                  {t('turns', { count })}
                </option>
              ))}
            </select>
          </label>
        </div>
        {error ? <Alert tone="warning">{error}</Alert> : null}
        <div className="flex flex-wrap justify-end gap-2">
          <Button variant="secondary" type="button" onClick={onClose}>
            {actions('cancel')}
          </Button>
          <Button type="submit" disabled={!ready || creating}>
            {creating ? t('creating') : t('create')}
          </Button>
        </div>
      </form>
    </ModalDialog>
  );
}
