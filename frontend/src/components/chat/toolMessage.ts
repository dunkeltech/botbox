// Tool lines stored by the engine keep the CLI text, including tool names and
// sandbox file paths. The chat shows a short sentence instead, and never the path.

export type ToolCopyKey =
  | 'toolSearch'
  | 'openPage'
  | 'openPageGeneric'
  | 'screenshotPage'
  | 'screenshotDesktop'
  | 'clickAt'
  | 'click'
  | 'typeText'
  | 'pressKey'
  | 'pressKeyGeneric'
  | 'scroll'
  | 'drag'
  | 'move'
  | 'wait'
  | 'fillField'
  | 'fillGeneric'
  | 'clickSelector'
  | 'readPage'
  | 'usingTool'
  | 'openedPage'
  | 'pageShot'
  | 'desktopShot'
  | 'clicked'
  | 'typed'
  | 'filled'
  | 'filledGeneric'
  | 'scrolled'
  | 'dragged'
  | 'moved'
  | 'pressed'
  | 'waited'
  | 'finished'
  | 'toolNote'
  | 'toolMemorySave'
  | 'toolMemorySearch'
  | 'toolMemoryUpdate'
  | 'toolMemoryDelete'
  | 'toolMemorySaved'
  | 'toolMemoryFound'
  | 'toolMemoryUpdated'
  | 'toolMemoryDeleted';

export type ToolCopy = {
  key: ToolCopyKey;
  values?: Record<string, string>;
  image: boolean;
};

const callName = /^[A-Za-z][\w-]*$/;

// A fresh expression each call. A shared /g pattern would remember lastIndex
// and skip the next screenshot. The shape matches the API allowlist.
function imageReference(): RegExp {
  return /\[Image: source: \/home\/bot\/\.claude\/projects\/-home-bot\/[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\/tool-results\/[A-Za-z0-9._-]{1,128}\.(?:png|jpe?g|webp|gif)\]/i;
}

/** describeToolMessage turns one stored tool row into chat copy. Non-tool rows stay raw. */
export function describeToolMessage(
  kind: string,
  content: string,
): ToolCopy | null {
  if (kind !== 'tool') return null;
  const pattern = imageReference();
  const image = pattern.test(content);
  const text = content.replace(imageReference(), '').replace(/\r/g, '').trim();
  const newline = text.indexOf('\n');
  if (newline > 0) {
    const name = text.slice(0, newline).trim();
    const rest = text.slice(newline + 1).trim();
    if (callName.test(name) && rest.startsWith('{')) {
      // Arguments may contain URLs. They are not rendered raw.
      return { ...describeCall(name, rest), image: false };
    }
  }
  // A result that still names a sandbox path is not shown. The image flag
  // stays off so a rejected path cannot be fetched.
  if (
    text.includes('/home/') ||
    text.includes('..') ||
    /\[Image: source:/i.test(text)
  ) {
    return { key: 'finished', image: false };
  }
  return { ...describeResult(text), image };
}

function describeCall(name: string, raw: unknown): Omit<ToolCopy, 'image'> {
  const leaf = name.split('__').pop() ?? name;
  let args: Record<string, unknown> = {};
  if (typeof raw === 'string') {
    try {
      const parsed: unknown = JSON.parse(raw);
      if (
        typeof parsed === 'object' &&
        parsed !== null &&
        !Array.isArray(parsed)
      ) {
        args = parsed as Record<string, unknown>;
      } else {
        return { key: 'finished' };
      }
    } catch {
      return { key: 'usingTool', values: { name: safeName(leaf) } };
    }
  }
  switch (leaf) {
    case 'ToolSearch':
      return { key: 'toolSearch' };
    case 'browser_navigate': {
      const url = pageURL(args.url);
      return url
        ? { key: 'openPage', values: { url } }
        : { key: 'openPageGeneric' };
    }
    case 'browser_screenshot':
      return { key: 'screenshotPage' };
    case 'screenshot':
      return { key: 'screenshotDesktop' };
    case 'browser_click': {
      const selector = shortLabel(args.selector);
      return selector
        ? { key: 'clickSelector', values: { selector } }
        : { key: 'click' };
    }
    case 'browser_fill': {
      const selector = shortLabel(args.selector);
      return selector
        ? { key: 'fillField', values: { selector } }
        : { key: 'fillGeneric' };
    }
    case 'browser_read':
      return { key: 'readPage' };
    case 'click': {
      const x = coord(args.x);
      const y = coord(args.y);
      return x && y ? { key: 'clickAt', values: { x, y } } : { key: 'click' };
    }
    case 'type':
      return { key: 'typeText' };
    case 'key': {
      const key = keyName(args.key);
      return key
        ? { key: 'pressKey', values: { key } }
        : { key: 'pressKeyGeneric' };
    }
    case 'scroll':
      return { key: 'scroll' };
    case 'drag':
      return { key: 'drag' };
    case 'mouse_move':
      return { key: 'move' };
    case 'wait':
      return { key: 'wait' };
    case 'memory_save':
      return { key: 'toolMemorySave' };
    case 'memory_search':
      return { key: 'toolMemorySearch' };
    case 'memory_update':
      return { key: 'toolMemoryUpdate' };
    case 'memory_delete':
      return { key: 'toolMemoryDelete' };
    default:
      return { key: 'usingTool', values: { name: safeName(leaf) } };
  }
}

function describeResult(text: string): Omit<ToolCopy, 'image'> {
  if (text.startsWith('{')) {
    try {
      const parsed: unknown = JSON.parse(text);
      if (typeof parsed === 'object' && parsed !== null) {
        if ('saved' in parsed && parsed.saved === true) {
          return { key: 'toolMemorySaved' };
        }
        if ('entries' in parsed && Array.isArray(parsed.entries)) {
          return { key: 'toolMemoryFound' };
        }
        if ('updated' in parsed && parsed.updated === true) {
          return { key: 'toolMemoryUpdated' };
        }
        if ('deleted' in parsed && parsed.deleted === true) {
          return { key: 'toolMemoryDeleted' };
        }
      }
    } catch {
      return { key: 'finished' };
    }
    return { key: 'finished' };
  }
  if (text === '' || text.startsWith('[')) {
    return { key: 'finished' };
  }
  const filled = /^filled (.+)$/.exec(text);
  if (filled) {
    const selector = shortLabel(filled[1]);
    return selector
      ? { key: 'filled', values: { selector } }
      : { key: 'filledGeneric' };
  }
  if (/^Captured \d+x\d+ screenshot$/.test(text) || text === 'screenshot') {
    return { key: 'desktopShot' };
  }
  switch (text) {
    case 'navigated':
      return { key: 'openedPage' };
    case 'browser screenshot':
      return { key: 'pageShot' };
    case 'clicked':
      return { key: 'clicked' };
    case 'typed':
      return { key: 'typed' };
    case 'scrolled':
      return { key: 'scrolled' };
    case 'dragged':
      return { key: 'dragged' };
    case 'moved':
      return { key: 'moved' };
    case 'pressed':
    case 'key':
      return { key: 'pressed' };
    case 'waited':
      return { key: 'waited' };
    default: {
      const note = shortLabel(text);
      return note ? { key: 'toolNote', values: { note } } : { key: 'finished' };
    }
  }
}

function pageURL(value: unknown): string | null {
  if (typeof value !== 'string' || value.length > 200 || /\s/.test(value)) {
    return null;
  }
  try {
    const url = new URL(value);
    if (url.protocol !== 'http:' && url.protocol !== 'https:') return null;
    return url.toString();
  } catch {
    return null;
  }
}

function shortLabel(value: unknown): string | null {
  if (typeof value !== 'string') return null;
  const trimmed = value.trim();
  if (!/^[\w .:#=[\]"'-]{1,80}$/.test(trimmed)) return null;
  return trimmed;
}

function coord(value: unknown): string | null {
  if (
    typeof value !== 'number' ||
    !Number.isInteger(value) ||
    value < 0 ||
    value > 10000
  ) {
    return null;
  }
  return String(value);
}

function keyName(value: unknown): string | null {
  if (typeof value !== 'string' || !/^[A-Za-z0-9+_-]{1,24}$/.test(value)) {
    return null;
  }
  return value;
}

function safeName(leaf: string): string {
  const words = leaf
    .replace(/_/g, ' ')
    .replace(/[^\w ]/g, '')
    .trim();
  if (!words || words.length > 40) return 'a tool';
  return words;
}
