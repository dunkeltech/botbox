import type { components } from '@/types/openapi.gen';

type Group = components['schemas']['Group'];
type Member = components['schemas']['GroupMember'];

export interface MentionTarget {
  id: string;
  name: string;
  kind: 'user' | 'bot';
}

/** The @ being typed at the caret, if it is not inside a word. */
export function activeMention(
  value: string,
  caret: number,
): { start: number; query: string } | null {
  const upto = value.slice(0, caret);
  const at = upto.lastIndexOf('@');
  if (at < 0) return null;
  const before = at > 0 ? upto[at - 1] : '';
  if (before && /[A-Za-z0-9]/.test(before)) return null;
  const query = upto.slice(at + 1);
  if (/\s/.test(query)) return null;
  return { start: at, query };
}

/** Replaces the active @query with @Name and leaves a trailing space. */
export function insertMention(
  value: string,
  start: number,
  caret: number,
  name: string,
): { value: string; caret: number } {
  const next = `${value.slice(0, start)}@${name} ${value.slice(caret)}`;
  return { value: next, caret: start + name.length + 2 };
}

export function filterByQuery<T extends { name: string }>(
  items: T[],
  query: string,
): T[] {
  const needle = query.trim().toLowerCase();
  if (!needle) return items;
  return items.filter((item) => item.name.toLowerCase().includes(needle));
}

/** Splits text so @Name tokens that match the roster can be marked. */
export function mentionParts(
  content: string,
  names: string[],
): { text: string; mention: boolean }[] {
  const sorted = [...new Set(names.filter((name) => name.trim() !== ''))].sort(
    (a, b) => b.length - a.length,
  );
  if (sorted.length === 0) return [{ text: content, mention: false }];
  const escaped = sorted.map((name) =>
    name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'),
  );
  const pattern = new RegExp(
    `(^|[^A-Za-z0-9])@(${escaped.join('|')})(?![A-Za-z0-9])`,
    'gi',
  );
  const parts: { text: string; mention: boolean }[] = [];
  let cursor = 0;
  for (const match of content.matchAll(pattern)) {
    const index = match.index ?? 0;
    const lead = match[1] ?? '';
    const name = match[2] ?? '';
    const nameAt = index + lead.length;
    if (nameAt > cursor) {
      parts.push({ text: content.slice(cursor, nameAt), mention: false });
    }
    parts.push({
      text: content.slice(nameAt, nameAt + name.length + 1),
      mention: true,
    });
    cursor = nameAt + name.length + 1;
  }
  if (cursor < content.length) {
    parts.push({ text: content.slice(cursor), mention: false });
  }
  return parts.length > 0 ? parts : [{ text: content, mention: false }];
}

/** Full replace body. A partial edit must not drop members or ask-before rules. */
export function groupSettingsBody(
  group: Group,
  patch: Partial<{
    name: string;
    goal: string;
    leadBotId: string;
    botTalk: boolean;
    turnLimit: number;
    answerPolicy: Group['answerPolicy'];
    members: Member[];
  }>,
): components['schemas']['UpdateGroupRequest'] {
  return {
    name: patch.name ?? group.name,
    goal: patch.goal ?? group.goal,
    leadBotId: patch.leadBotId ?? group.leadBotId,
    botTalk: patch.botTalk ?? group.botTalk,
    turnLimit: patch.turnLimit ?? group.turnLimit,
    answerPolicy: patch.answerPolicy ?? group.answerPolicy,
    askBefore: group.askBefore,
    budgetLimitMicros: group.budgetLimitMicros ?? null,
    members: (patch.members ?? group.members).map((member) => ({
      type: member.type,
      id: member.id,
      roleText: member.roleText,
    })),
  };
}

export function initials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean);
  const letters = parts.slice(0, 2).map((part) => part[0] ?? '');
  const text = letters.join('').toUpperCase();
  return text || '?';
}
