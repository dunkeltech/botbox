import { mentionParts } from '@/components/team/text';

interface MdastNode {
  type: string;
  value?: string;
  children?: MdastNode[];
  data?: {
    hName?: string;
    hProperties?: Record<string, unknown>;
    hChildren?: MdastNode[];
  };
}

function splitTextNode(value: string, mentionNames: string[]): MdastNode[] {
  const lines = value.split(/\r?\n/);
  const nodes: MdastNode[] = [];
  for (let i = 0; i < lines.length; i += 1) {
    if (i > 0) {
      nodes.push({ type: 'break' });
    }
    const line = lines[i] ?? '';
    if (line === '') continue;
    if (mentionNames.length === 0) {
      nodes.push({ type: 'text', value: line });
      continue;
    }
    for (const part of mentionParts(line, mentionNames)) {
      if (part.text === '') continue;
      if (part.mention) {
        nodes.push({
          type: 'chatMention',
          value: part.text,
          data: {
            hName: 'span',
            hProperties: { 'data-mention': 'true' },
            hChildren: [{ type: 'text', value: part.text }],
          },
        });
      } else {
        nodes.push({ type: 'text', value: part.text });
      }
    }
  }
  return nodes;
}

function visitChildren(node: MdastNode, mentionNames: string[]): void {
  if (!Array.isArray(node.children)) return;
  const nextChildren: MdastNode[] = [];
  for (const child of node.children) {
    if (child.type === 'text' && typeof child.value === 'string') {
      nextChildren.push(...splitTextNode(child.value, mentionNames));
      continue;
    }
    // Code blocks, inline code, and mentions keep literal text without
    // injecting line breaks or @mention badges.
    if (
      child.type !== 'code' &&
      child.type !== 'inlineCode' &&
      child.type !== 'chatMention'
    ) {
      visitChildren(child, mentionNames);
    }
    nextChildren.push(child);
  }
  node.children = nextChildren;
}

/**
 * Remark plugin that turns single chat newlines into line breaks and highlights
 * roster @mentions inside prose without altering code blocks or link targets.
 */
export function remarkChatText(mentionNames: string[] = []) {
  const names = mentionNames.filter((name) => name.trim() !== '');
  return function transform(tree: MdastNode): void {
    visitChildren(tree, names);
  };
}
