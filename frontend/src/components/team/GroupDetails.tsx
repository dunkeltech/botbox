'use client';

import Link from 'next/link';
import { useTranslations } from 'next-intl';
import { Alert, BotAvatar } from '@/components/ui';
import type { Bot, BotStatus } from '@/types/api';
import type { components } from '@/types/openapi.gen';
import { initials } from '@/components/team/text';
import { TalkSwitch } from '@/components/team/TalkSwitch';
import { BotRoleDescription } from '@/components/team/BotRoleDescription';

type Group = components['schemas']['Group'];
type AskBefore = components['schemas']['AskBeforeRule'];
type Policy = Group['answerPolicy'];

const PRESET_TURNS = [10, 20, 50];

function statusKey(
  status: BotStatus,
): 'statusWorking' | 'statusThinking' | 'statusIdle' | 'statusStopped' {
  if (status === 'action') return 'statusWorking';
  if (status === 'thinking') return 'statusThinking';
  if (status === 'stopped') return 'statusStopped';
  return 'statusIdle';
}

function moodFor(
  status: BotStatus,
): 'action' | 'thinking' | 'idle' | 'offline' {
  if (status === 'action') return 'action';
  if (status === 'thinking') return 'thinking';
  if (status === 'stopped') return 'offline';
  return 'idle';
}

function askText(
  rule: AskBefore,
  t: (key: 'askNewHost' | 'askBudget' | 'askPush' | 'askBefore') => string,
): string {
  if (rule.kind === 'new_host') return t('askNewHost');
  if (rule.kind === 'raise_budget') return t('askBudget');
  if (rule.kind === 'push_protected') return t('askPush');
  return rule.label?.trim() || t('askBefore');
}

function policyText(
  policy: Policy,
  t: (key: 'policyMentioned' | 'policyLead' | 'policyRound') => string,
): string {
  if (policy === 'lead_first') return t('policyLead');
  if (policy === 'round_robin') return t('policyRound');
  return t('policyMentioned');
}

/** Group goal, members, and the settings a member can change. */
export function GroupDetails({
  group,
  bots,
  canEdit,
  error,
  onPatch,
}: {
  group: Group;
  bots: Bot[];
  canEdit: boolean;
  error: string | null;
  onPatch: (patch: {
    botTalk?: boolean;
    turnLimit?: number;
    answerPolicy?: Policy;
  }) => void;
}) {
  const t = useTranslations('team');
  const turnOptions = PRESET_TURNS.includes(group.turnLimit)
    ? PRESET_TURNS
    : [...PRESET_TURNS, group.turnLimit].sort((a, b) => a - b);
  const botMembers = group.members.filter((member) => member.type === 'bot');

  return (
    <div className="flex flex-col gap-5">
      <section className="flex flex-col gap-1.5">
        <h2 className="text-[12px] font-bold uppercase tracking-[0.08em] text-muted">
          {t('goal')}
        </h2>
        <p className="text-[15px]">{group.goal.trim() || t('noGoal')}</p>
      </section>

      <section className="flex flex-col gap-2">
        <h2 className="text-[12px] font-bold uppercase tracking-[0.08em] text-muted">
          {t('members')}
        </h2>
        <ul className="flex flex-col gap-2">
          {group.members.map((member) => {
            const bot = bots.find((row) => row.id === member.id);
            const name =
              member.name?.trim() ||
              (member.type === 'bot' ? t('bot') : t('human'));
            const line =
              member.id === group.leadBotId
                ? t('lead')
                : member.roleText.trim() ||
                  (member.type === 'bot' ? t('bot') : t('human'));
            return (
              <li key={member.id} className="flex items-center gap-2.5">
                {member.type === 'bot' ? (
                  <BotAvatar
                    seed={bot?.avatarSeed || member.id}
                    mood={bot ? moodFor(bot.status) : 'idle'}
                    size={34}
                    showStatusDot={Boolean(bot)}
                    decorative
                  />
                ) : (
                  <span className="grid h-9 w-9 shrink-0 place-items-center rounded-sm bg-hex-2 text-[12px] font-black text-paper">
                    {initials(name)}
                  </span>
                )}
                <span className="min-w-0">
                  <span className="block font-black">{name}</span>
                  <span className="block text-[13px] text-muted">
                    {line}
                    {bot ? ` · ${t(statusKey(bot.status))}` : ''}
                  </span>
                </span>
              </li>
            );
          })}
        </ul>
      </section>

      <section className="flex flex-col gap-3">
        <h2 className="text-[12px] font-bold uppercase tracking-[0.08em] text-muted">
          {t('howBotsTalk')}
        </h2>
        {canEdit ? (
          <TalkSwitch
            checked={group.botTalk}
            label={t('botsMention')}
            onToggle={() => onPatch({ botTalk: !group.botTalk })}
            className="justify-between"
          />
        ) : (
          <p className="text-[14px] font-bold">
            {group.botTalk ? t('botTalkOn') : t('botTalkOff')}
          </p>
        )}
        {canEdit ? (
          <label className="flex flex-col gap-1.5 text-[14px] font-bold">
            {t('turnLimit')}
            <select
              className="min-h-11 rounded-sm border-[1.5px] border-line-strong bg-paper px-3 text-[15px] font-normal focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
              value={String(group.turnLimit)}
              onChange={(event) =>
                onPatch({ turnLimit: Number(event.target.value) })
              }
            >
              {turnOptions.map((count) => (
                <option key={count} value={count}>
                  {t('turns', { count })}
                </option>
              ))}
            </select>
          </label>
        ) : (
          <p className="text-[14px]">
            <span className="font-bold">{t('turnLimit')}</span>
            {` · ${t('turns', { count: group.turnLimit })}`}
          </p>
        )}
        {canEdit ? (
          <label className="flex flex-col gap-1.5 text-[14px] font-bold">
            {t('whoAnswers')}
            <select
              className="min-h-11 rounded-sm border-[1.5px] border-line-strong bg-paper px-3 text-[15px] font-normal focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
              value={group.answerPolicy}
              onChange={(event) =>
                onPatch({ answerPolicy: event.target.value as Policy })
              }
            >
              <option value="mentioned_else_lead">
                {t('policyMentioned')}
              </option>
              <option value="lead_first">{t('policyLead')}</option>
              <option value="round_robin">{t('policyRound')}</option>
            </select>
          </label>
        ) : (
          <p className="text-[14px]">
            <span className="font-bold">{t('whoAnswers')}</span>
            {` · ${policyText(group.answerPolicy, t)}`}
          </p>
        )}
        <div className="flex flex-col gap-1.5">
          <span className="text-[14px] font-bold">{t('askBefore')}</span>
          {group.askBefore.length > 0 ? (
            <ul className="flex flex-wrap gap-1.5">
              {group.askBefore.map((rule) => (
                <li
                  key={`${rule.kind}:${rule.label ?? ''}`}
                  className="rounded-pill border border-line bg-paper px-2.5 py-1 text-[13px]"
                >
                  {askText(rule, t)}
                </li>
              ))}
            </ul>
          ) : (
            <p className="text-[14px] text-muted">{t('notSet')}</p>
          )}
        </div>
        {error ? <Alert tone="warning">{error}</Alert> : null}
      </section>

      <section className="flex flex-col gap-2">
        <h2 className="text-[12px] font-bold uppercase tracking-[0.08em] text-muted">
          {t('computers')}
        </h2>
        <ul className="flex flex-col gap-2">
          {botMembers.map((member) => {
            const bot = bots.find((row) => row.id === member.id);
            const name = member.name?.trim() || bot?.name || t('bot');
            return (
              <li key={member.id}>
                <Link
                  href={`/bots/${member.id}`}
                  className="flex min-h-11 items-center gap-2 rounded-sm border border-line bg-paper px-3 text-[14px] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
                >
                  <span className="font-black">{name}</span>
                  <span className="text-green-600">{t('openComputer')}</span>
                </Link>
              </li>
            );
          })}
        </ul>
      </section>
    </div>
  );
}

/** Direct-message details: the bot computer and the groups that include it. */
export function DirectDetails({
  bot,
  groups,
  canCreate,
  onOpenGroup,
  onNewGroup,
}: {
  bot: Bot;
  groups: Group[];
  canCreate: boolean;
  onOpenGroup: (groupId: string) => void;
  onNewGroup: () => void;
}) {
  const t = useTranslations('team');
  const mine = groups.filter((group) =>
    group.members.some(
      (member) => member.type === 'bot' && member.id === bot.id,
    ),
  );
  const brain = [bot.llmProvider, bot.llmModel].filter(Boolean).join(' · ');
  return (
    <div className="flex flex-col gap-5">
      <section className="flex flex-col items-start gap-2">
        <BotAvatar
          seed={bot.avatarSeed}
          mood={moodFor(bot.status)}
          size={72}
          showStatusDot
        />
        <h2 className="text-[22px] font-black">{bot.name}</h2>
        <p className="text-[14px] font-bold">{t(statusKey(bot.status))}</p>
        {bot.description?.trim() ? (
          <BotRoleDescription
            key={`${bot.id}:${bot.description}`}
            content={bot.description}
          />
        ) : null}
        <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-[14px]">
          <dt className="font-bold text-muted">{t('brain')}</dt>
          <dd className="break-all font-mono text-[12px]">
            {brain || t('notSet')}
          </dd>
        </dl>
        <div className="flex flex-wrap gap-2">
          <Link
            href={`/bots/${bot.id}`}
            className="inline-flex min-h-11 items-center rounded-sm bg-green-600 px-4 font-bold text-paper focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
          >
            {t('openComputer')}
          </Link>
          <Link
            href={`/bots/${bot.id}/settings`}
            className="inline-flex min-h-11 items-center rounded-sm border-[1.5px] border-line bg-paper px-4 font-bold text-green-600 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
          >
            {t('botSettings')}
          </Link>
        </div>
      </section>
      <section className="flex flex-col gap-2">
        <h2 className="text-[12px] font-bold uppercase tracking-[0.08em] text-muted">
          {t('dmGroups')}
        </h2>
        {mine.length === 0 ? (
          <p className="text-[14px] text-muted">{t('dmNoGroups')}</p>
        ) : (
          <ul className="flex flex-col gap-2">
            {mine.map((group) => (
              <li key={group.id}>
                <button
                  type="button"
                  onClick={() => onOpenGroup(group.id)}
                  className="flex min-h-11 w-full items-center gap-2 rounded-sm border border-line bg-paper px-3 text-left text-[14px] font-bold focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
                >
                  <span aria-hidden="true" className="text-muted">
                    #
                  </span>
                  {group.name}
                </button>
              </li>
            ))}
          </ul>
        )}
        {canCreate ? (
          <button
            type="button"
            onClick={onNewGroup}
            className="inline-flex min-h-11 items-center self-start rounded-sm border-[1.5px] border-line bg-paper px-3 text-[14px] font-bold text-green-600 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
          >
            {t('newGroupWith', { name: bot.name })}
          </button>
        ) : null}
      </section>
    </div>
  );
}
