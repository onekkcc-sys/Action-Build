// End-to-end UI test for Agent Hub.
// Usage: node tools/ui-e2e.js <base-url> [screenshot-dir] [browser-channel]
//   e.g. node tools/ui-e2e.js http://127.0.0.1:17321 shots msedge
const { chromium } = require('playwright');

const base = (process.argv[2] || 'http://127.0.0.1:17321').replace(/\/$/, '');
const shots = process.argv[3] || '.';
const channel = process.argv[4];

let step = 0;
function ok(cond, msg) {
  step++;
  if (!cond) throw new Error(`FAIL #${step}: ${msg}`);
  console.log(`  ok ${step} - ${msg}`);
}

(async () => {
  const browser = await chromium.launch(channel ? { channel } : {});
  const page = await browser.newPage({ viewport: { width: 1360, height: 900 } });
  const errors = [];
  page.on('pageerror', e => errors.push(e.message));
  page.on('console', m => m.type() === 'error' && errors.push(m.text()));
  page.on('dialog', d => d.accept());
  const api = (method, path, body) => page.evaluate(async ([m, p, b]) => {
    const r = await fetch(p, { method: m, headers: { 'Content-Type': 'application/json', 'X-Agent-Hub': '1' }, body: b ? JSON.stringify(b) : undefined });
    return r.json();
  }, [method, path, body]);
  const cardOf = name => page.locator('.card', { has: page.locator('h3', { hasText: new RegExp(`^${name}`) }) });

  try {
    await page.goto(base + '/#/usage');
    await page.waitForSelector('.card');
    ok(await page.title() === 'Agent Hub · 账号管家', 'page loads');
    ok(await page.locator('#agentList li a').count() >= 1, 'sidebar lists accounts');

    // --- create account through the dialog
    await page.click('#addBtn');
    await page.fill('input[name=name]', 'E2E 1');
    await page.fill('input[name=agent]', '阿测');
    await page.fill('input[name=plan]', 'Max 20x');
    await page.click('dialog button[type=submit]');
    await page.waitForFunction(() => location.hash.startsWith('#/account/'));
    ok((await page.textContent('.detail-head h2')).includes('阿测 · E2E 1'), 'new account opens its detail page');
    ok(await page.locator('#agentList .title', { hasText: '阿测 · E2E 1' }).count() === 1, 'new account shows in sidebar');
    const id = (await page.evaluate(() => location.hash)).split('/')[2];

    // --- enter a reading: 5h left 40 in 2h, weekly used 70 in 3d (approx)
    await page.click('.btn-row [data-usage]');
    await page.fill('input[name=fhVal]', '40');
    await page.fill('input[name=fhReset]', '2h');
    ok((await page.textContent('[data-preview=fh]')).includes('还有 2 小时'), 'reset time preview understands "2h"');
    await page.fill('input[name=fhReset]', 'xyz');
    ok((await page.textContent('[data-preview=fh]')).includes('无法识别'), 'preview flags unparsable time');
    await page.fill('input[name=fhReset]', '2h');
    await page.click('[data-seg=wk] [data-m=used]');
    await page.fill('input[name=wkVal]', '70');
    await page.fill('input[name=wkReset]', '3d');
    await page.check('input[name=wkApprox]');
    await page.click('dialog button[type=submit]');
    await page.waitForSelector('.card .win');
    let card = (await page.textContent('.card')).replace(/\s+/g, ' ');
    ok(card.includes('剩 40%') && card.includes('剩 30%'), `card shows 40% / 30% (${card.slice(0, 120)})`);
    ok(card.includes('手动录入') && card.includes('约 '), 'card shows source and approximate weekly reset');

    const cd1 = await page.textContent('[data-cd]');
    const t0 = Date.now();
    const changed = await page.waitForFunction(t => document.querySelector('[data-cd]')?.textContent !== t, cd1, { timeout: 6000 })
      .then(() => true, () => false);
    const cd2 = await page.textContent('[data-cd]');
    const diag = await page.evaluate(() => ({ skew: clockSkew, msLeft: +document.querySelector('[data-cd]').dataset.cd - Date.now(), visibility: document.visibilityState }));
    ok(changed && /还有 01:5\d:\d\d/.test(cd2), `countdown ticks (${cd1} → ${cd2} after ${Date.now() - t0}ms) ${JSON.stringify(diag)}`);

    // --- message board
    await page.fill('#msgInput', '交接：先跑完测试');
    await page.keyboard.press('Control+Enter');
    await page.waitForSelector('.msgs li');
    ok((await page.textContent('.msgs')).includes('交接：先跑完测试'), 'message saved with Ctrl+Enter');

    // --- edit (rename)
    await page.click('[data-edit]');
    await page.fill('input[name=agent]', '阿改');
    await page.click('dialog button[type=submit]');
    await page.waitForFunction(() => document.querySelector('.detail-head h2').textContent.includes('阿改'));
    ok(await page.locator('#agentList .title', { hasText: '阿改 · E2E 1' }).count() === 1, 'rename updates sidebar');

    // --- states: possibly recovered, stale, no 5h window, low
    const now = Date.now(), H = 3600e3;
    await api('POST', `/api/accounts/${id}/usage`, { fiveHour: { left: 12, reset: now - H }, weekly: { left: 8, reset: now + 30 * H }, readAt: now - 10 * H });
    await page.goto(base + '/#/usage');
    await page.reload(); // hash-only navigation keeps the old in-memory state
    await page.waitForSelector('.card');
    card = (await cardOf('E2E 1').textContent()).replace(/\s+/g, ' ');
    ok(card.includes('重置前剩 12%') && card.includes('可能已恢复（未验证）'), 'passed 5h reset shows "possibly recovered"');
    ok(await cardOf('E2E 1').locator('.badge-old').count() === 1 && card.includes('· 旧'), 'old reading is marked 旧');
    ok(await cardOf('E2E 1').locator('.bar.low').count() === 1, 'weekly 8% bar uses low colour');
    await api('POST', `/api/accounts/${id}/usage`, { noFiveHour: true, weekly: { left: 92, reset: now + 100 * H } });
    await page.reload();
    await page.waitForSelector('.card');
    ok((await cardOf('E2E 1').textContent()).includes('这个套餐现在没有 5 小时窗口'), 'no-5h plan message');

    // --- report API (token) shows as 会话上报
    const st = await api('GET', '/api/state');
    const rep = await page.evaluate(async ([tok]) => {
      const r = await fetch('/api/report', { method: 'POST', headers: { 'X-Token': tok, 'Content-Type': 'application/json' }, body: JSON.stringify({ account: 'E2E 1', five_hour: { used: 23, resets_in_seconds: 7200 }, weekly: { left: 66 } }) });
      return r.status;
    }, [st.settings.token]);
    ok(rep === 200, 'report API accepts token');
    await page.click('#scanBtn');
    await page.waitForTimeout(500);
    card = (await cardOf('E2E 1').textContent()).replace(/\s+/g, ' ');
    ok(card.includes('会话上报') && card.includes('剩 77%') && card.includes('剩 66%'), 'reported reading displayed');

    // --- filters & sorting
    await page.click('[data-filter=codex]');
    ok(await cardOf('E2E 1').count() === 0, 'Codex filter hides Claude cards');
    await page.click('[data-filter=all]');
    await page.click('[data-sort=left]');
    ok(await page.locator('.card').count() >= 2, 'sort by remaining keeps all cards');
    ok(await page.locator('.reco').count() === 1, 'recommendation shown');
    await page.screenshot({ path: `${shots}/e2e-usage.png` });

    // --- decisions
    await page.click('[data-nav=decisions]');
    await page.fill('#decInput', 'E2E 决定');
    await page.selectOption('#decAcc', id);
    await page.keyboard.press('Enter');
    await page.waitForSelector('.dec-list li');
    const before = +(await page.textContent('#decCount') || 0);
    ok(before >= 1, 'decision added and counted');
    await page.locator('.dec-list li', { hasText: 'E2E 决定' }).locator('[data-toggle]').click();
    await page.waitForTimeout(300);
    const after = +(await page.textContent('#decCount') || 0);
    ok(after === before - 1, 'toggling marks decision done');

    // --- search
    await page.click('#searchBtn');
    await page.fill('#searchInput', '阿改');
    ok(await page.locator('#agentList li a').count() === 1, 'sidebar search filters');
    await page.fill('#searchInput', 'zzzz-none');
    ok((await page.textContent('#agentList')).includes('没有匹配'), 'search empty state');
    await page.click('#searchBtn');

    // --- settings
    await page.click('#settingsBtn');
    ok((await page.inputValue('#tokenField')) === st.settings.token, 'settings shows token');
    await page.fill('input[name=staleHours]', '2');
    await page.click('dialog button[type=submit]');
    await page.waitForTimeout(300);
    ok((await api('GET', '/api/state')).settings.staleHours === 2, 'settings saved');
    await page.screenshot({ path: `${shots}/e2e-decisions.png` });

    // --- delete account
    await page.goto(`${base}/#/account/${id}`);
    await page.click('[data-edit]');
    await page.click('#delAcc');
    await page.waitForFunction(() => location.hash === '#/usage');
    ok(await page.locator('#agentList .title', { hasText: 'E2E 1' }).count() === 0, 'account deleted');

    ok(errors.length === 0, `no browser errors ${errors.length ? JSON.stringify(errors) : ''}`);
    console.log(`UI E2E PASSED (${step} checks)`);
  } catch (e) {
    await page.screenshot({ path: `${shots}/e2e-failure.png` }).catch(() => {});
    console.error(e.message, errors.length ? `\nbrowser errors: ${JSON.stringify(errors)}` : '');
    process.exitCode = 1;
  } finally {
    await browser.close();
  }
})();
