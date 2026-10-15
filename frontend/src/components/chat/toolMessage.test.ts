import { describe, expect, it } from 'vitest';
import { describeToolMessage } from '@/components/chat/toolMessage';

const shot =
  '[Image: source: /home/bot/.claude/projects/-home-bot/fe505481-b639-48b3-95ab-6e1db702084f/tool-results/mcp-botbox-desktop-blob-1794831139043-dsdyks.png]';

describe('describeToolMessage', () => {
  it('turns desktop tool calls into short sentences', () => {
    expect(
      describeToolMessage(
        'tool',
        'ToolSearch\n{"query":"select:mcp__botbox-desktop__browser_navigate","max_results":2}',
      ),
    ).toEqual({ key: 'toolSearch', image: false });
    expect(
      describeToolMessage(
        'tool',
        'mcp__botbox-desktop__browser_navigate\n{"url":"https://dunkel.tech"}',
      ),
    ).toEqual({
      key: 'openPage',
      values: { url: 'https://dunkel.tech/' },
      image: false,
    });
    expect(
      describeToolMessage(
        'tool',
        'mcp__botbox-desktop__browser_screenshot\n{}',
      ),
    ).toEqual({ key: 'screenshotPage', image: false });
    expect(describeToolMessage('tool', 'navigated')).toEqual({
      key: 'openedPage',
      image: false,
    });
  });

  it('marks a real tool-result image and hides the sandbox path', () => {
    const copy = describeToolMessage('tool', `${shot}\nbrowser screenshot`);
    expect(copy).toEqual({ key: 'pageShot', image: true });
    expect(JSON.stringify(copy)).not.toContain('tool-results');
    expect(JSON.stringify(copy)).not.toContain('/home/bot');
  });

  it('rejects paths outside the tool-results directory', () => {
    const cases = [
      '[Image: source: /home/bot/.ssh/id_rsa]',
      '[Image: source: /home/bot/.claude/projects/-home-bot/fe505481-b639-48b3-95ab-6e1db702084f/tool-results/../../.ssh/id_rsa.png]',
      '[Image: source: /etc/passwd]',
      '[Image: source: javascript:alert(1)]',
      '',
    ];
    for (const content of cases) {
      const copy = describeToolMessage('tool', content);
      expect(copy?.image).toBe(false);
      expect(JSON.stringify(copy)).not.toContain('/home/bot');
      expect(JSON.stringify(copy)).not.toContain('javascript');
    }
  });

  it('does not treat a page address as a link target when it is not http', () => {
    expect(
      describeToolMessage(
        'tool',
        'mcp__botbox-desktop__browser_navigate\n{"url":"javascript:alert(1)"}',
      ),
    ).toEqual({ key: 'openPageGeneric', image: false });
    expect(
      describeToolMessage(
        'tool',
        'mcp__botbox-desktop__browser_navigate\n{"url":"https://evil.example/a b"}',
      ),
    ).toEqual({ key: 'openPageGeneric', image: false });
  });

  it('leaves ordinary chat text unchanged', () => {
    expect(describeToolMessage('text', 'hello')).toBeNull();
    expect(describeToolMessage('tool', '   ')).toEqual({
      key: 'finished',
      image: false,
    });
  });
});
