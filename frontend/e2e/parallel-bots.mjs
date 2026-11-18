// SPDX-License-Identifier: AGPL-3.0-only

import assert from 'node:assert/strict';
import { pathToFileURL, URL } from 'node:url';

// Use the driver and matching browser declared in devenv, never a global install.
const modulePath = process.env.BOTBOX_PLAYWRIGHT_MODULE;
assert.ok(modulePath, 'Run this browser test inside devenv shell');
const { chromium } = await import(
  pathToFileURL(`${modulePath}/index.mjs`).href
);
const browser = await chromium.launch({ headless: true });
try {
  const page = await browser.newPage({
    viewport: { width: 1440, height: 1000 },
  });
  page.setDefaultTimeout(10000);
  let stage = 0;
  let socket;
  const now = '2026-11-17T12:00:00Z';
  const workspace = {
    id: 'ws-fixture',
    name: 'Fixture',
    slug: 'fixture',
    role: 'owner',
    defaultDriver: 'docker',
    defaultLocale: 'en',
    createdAt: now,
  };
  const user = {
    id: 'user-fixture',
    email: 'fixture@example.com',
    displayName: 'Fixture',
    locale: 'en',
    createdAt: now,
  };
  const bots = ['Yoda', 'Nova', 'Atlas'].map((name, i) => ({
    id: `bot-${i}`,
    workspaceId: workspace.id,
    name,
    description: '',
    avatarSeed: `fixture-${i}`,
    llmMode: 'cli',
    llmProvider: 'openrouter',
    llmModel: 'openrouter/fixture',
    driverType: 'docker',
    replyLanguage: 'en',
    status: 'idle',
    mood: 'idle',
    createdAt: now,
    updatedAt: now,
  }));
  const group = {
    id: 'group-fixture',
    name: 'Parallel',
    goal: 'Check concurrent activity',
    leadBotId: bots[0].id,
    botTalk: false,
    turnLimit: 20,
    answerPolicy: 'mentioned_else_lead',
    askBefore: [],
    paused: false,
    pauseReason: '',
    turnsSinceHuman: 0,
    unread: 0,
    members: [
      { type: 'user', id: user.id, name: user.displayName, roleText: '' },
      ...bots.map((b, i) => ({
        type: 'bot',
        id: b.id,
        name: b.name,
        roleText: i === 0 ? 'lead' : 'reviewer',
      })),
    ],
  };
  await page.route('**/api/**', async (route) => {
    const path = new URL(route.request().url()).pathname;
    let body = {};
    if (path.endsWith('/setup/status')) body = { setupRequired: false };
    else if (path.endsWith('/auth/refresh'))
      body = {
        user,
        workspaces: [workspace],
        activeWorkspace: workspace,
        accessToken: 'fixture-token',
        expiresIn: 3600,
      };
    else if (path.endsWith('/groups')) body = [group];
    else if (path.endsWith('/bots'))
      body = {
        bots: bots.map((b, i) => ({
          ...b,
          status:
            stage === 1 || (stage === 2 && i !== 0) || (stage === 4 && i === 1)
              ? 'thinking'
              : 'idle',
        })),
      };
    else if (path.endsWith('/members'))
      body = {
        members: [
          {
            workspaceId: workspace.id,
            userId: user.id,
            email: user.email,
            displayName: user.displayName,
            locale: 'en',
            role: 'owner',
            joinedAt: now,
          },
        ],
      };
    else if (path.endsWith('/messages'))
      body = {
        messages: [],
        turns: bots.map((b, i) => ({
          id: `turn-${i}`,
          botId: b.id,
          status:
            stage === 0
              ? 'queued'
              : stage === 1 ||
                  (stage === 2 && i !== 0) ||
                  (stage === 4 && i === 1)
                ? 'running'
                : stage === 3 && i === 1
                  ? 'failed'
                  : 'completed',
          queueAhead: 0,
          errorCode: stage === 3 && i === 1 ? 'engine.provider_failed' : '',
        })),
      };
    else if (path.endsWith('/approvals')) body = { approvals: [] };
    else if (path.endsWith('/notifications')) body = { notifications: [] };
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(body),
    });
  });
  await page.routeWebSocket('**/ws/chat/**', (ws) => {
    socket = ws;
  });
  const url = new URL(
    '/team',
    process.env.BOTBOX_E2E_URL || 'http://127.0.0.1:3000',
  );
  await page.goto(url.href);
  await page.getByText('Yoda is queued…', { exact: true }).waitFor();
  stage = 1;
  socket.send(
    JSON.stringify({
      Kind: 'status',
      BotID: 'bot-1',
      TurnID: 'turn-1',
      Status: 'thinking',
    }),
  );
  for (const bot of bots)
    await page.getByText(`${bot.name} is thinking…`, { exact: true }).waitFor();
  assert.ok((await page.getByText('Thinking', { exact: true }).count()) >= 3);
  await page.screenshot({
    path: '/tmp/botbox-parallel-status.png',
    fullPage: true,
  });
  stage = 2;
  // An old idle signal is an invalidation hint, not the source of current status.
  socket.send(
    JSON.stringify({
      Kind: 'status',
      BotID: 'bot-1',
      TurnID: 'old-turn',
      Status: 'idle',
    }),
  );
  await page
    .getByText('Yoda is thinking…', { exact: true })
    .waitFor({ state: 'hidden' });
  await page.getByText('Nova is thinking…', { exact: true }).waitFor();
  await page.getByText('Atlas is thinking…', { exact: true }).waitFor();
  await page.reload();
  await page.getByText('Nova is thinking…', { exact: true }).waitFor();
  assert.equal(
    await page.getByText('Yoda is thinking…', { exact: true }).count(),
    0,
  );
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByText('Atlas is thinking…', { exact: true }).waitFor();
  stage = 3;
  socket.send(
    JSON.stringify({
      Kind: 'status',
      BotID: 'bot-1',
      TurnID: 'turn-1',
      Status: 'idle',
    }),
  );
  await page
    .getByText('Nova could not finish this turn.', { exact: true })
    .waitFor();
  await page
    .getByText('The model provider could not complete the request.', {
      exact: false,
    })
    .waitFor();
  stage = 4;
  socket.send(
    JSON.stringify({
      Kind: 'status',
      BotID: 'bot-1',
      TurnID: 'retry-turn',
      Status: 'thinking',
    }),
  );
  await page
    .getByText('Nova could not finish this turn.', { exact: true })
    .waitFor({ state: 'hidden' });
  await page.getByText('Nova is thinking…', { exact: true }).waitFor();
  console.log(
    'Passed: queued fan-out, three active bots, independent completion, stale event, reload, mobile layout, group failure, retry',
  );
} finally {
  await browser.close();
}
