'use strict';

/* ================= state & api ================= */
let S = { accounts: [], decisions: [], settings: { staleHours: 6, lowPercent: 15 }, running: {}, scanErrs: {} };
let clockSkew = 0; // server now - local now
const ui = {
  filter: localStorage.getItem('ah.filter') || 'all',
  sort: localStorage.getItem('ah.sort') || 'order',
  search: '',
  groupOpen: localStorage.getItem('ah.group') !== '0',
};
const PALETTE = ['#e2623b', '#e8a21b', '#e27aa8', '#d63a3a', '#b8754a', '#2f78d6', '#2ea86b', '#7a5cd6', '#3aa7b8', '#6b6b6b'];
const PROVIDERS = { claude: 'Claude', codex: 'Codex', other: '其他' };
const STATUS = { online: '在线', offline: '离线 · 可先留言', unlinked: '还没接入' };

const $ = (s, el = document) => el.querySelector(s);
const $$ = (s, el = document) => [...el.querySelectorAll(s)];
const esc = s => String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const now = () => Date.now() + clockSkew;
const pad = n => String(n).padStart(2, '0');

async function api(method, path, body) {
  const r = await fetch(path, {
    method,
    headers: { 'Content-Type': 'application/json', 'X-Agent-Hub': '1' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const j = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(j.error || r.statusText);
  if (j.accounts) applyState(j);
  return j;
}

function applyState(j) {
  S = j;
  if (j.now) clockSkew = j.now - Date.now();
  render();
}

async function refresh() {
  // don't wipe what the user is typing
  if (dlg.open || document.activeElement?.matches('#main input, #main textarea, #main select')) return;
  try { await api('GET', '/api/state'); } catch (e) { /* server may be restarting */ }
}

function toast(msg, err) {
  const t = $('#toast');
  t.textContent = msg;
  t.className = 'show' + (err ? ' err' : '');
  clearTimeout(toast.t);
  toast.t = setTimeout(() => (t.className = ''), err ? 4000 : 2200);
}

async function act(fn, okMsg) {
  try {
    await fn();
    if (okMsg) toast(okMsg);
  } catch (e) {
    toast(e.message || String(e), true);
  }
}

/* ================= time helpers ================= */
function fmtDate(ms) {
  const d = new Date(ms);
  return `${d.getMonth() + 1}月${d.getDate()}日 ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}
function fmtHM(ms) { const d = new Date(ms); return `${pad(d.getHours())}:${pad(d.getMinutes())}`; }
function sameDay(a, b) { const x = new Date(a), y = new Date(b); return x.toDateString() === y.toDateString(); }
function fmtRead(ms) { return sameDay(ms, now()) ? fmtHM(ms) : fmtDate(ms); }
function fmtShortDate(ms) { const d = new Date(ms); return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${fmtHM(ms)}`; }
function rel(ms) {
  const s = Math.max(0, (now() - ms) / 1000);
  if (s < 60) return '刚刚';
  if (s < 3600) return `${Math.floor(s / 60)} 分钟前`;
  if (s < 48 * 3600) return `${Math.floor(s / 3600)} 小时前`;
  return `${Math.floor(s / 86400)} 天前`;
}
function relBare(ms) { return rel(ms).replace(/前$/, ''); }
function countdown(ms) {
  let s = Math.max(0, Math.floor((ms - now()) / 1000));
  const d = Math.floor(s / 86400); s %= 86400;
  const t = `${pad(Math.floor(s / 3600))}:${pad(Math.floor(s / 60) % 60)}:${pad(s % 60)}`;
  return d > 0 ? `${d}天 ${t}` : t;
}
function humanDur(ms) {
  const m = Math.round(ms / 60000);
  if (m < 60) return `${m} 分钟`;
  const h = Math.floor(m / 60), mm = m % 60;
  if (h < 24) return `${h} 小时${mm ? ` ${mm} 分` : ''}`;
  return `${Math.floor(h / 24)} 天 ${h % 24} 小时`;
}

/* Parse things like "2h30m", "2小时30分", "90m", "3d", "09:00", "明天 9:00",
   "10-04 09:00", "2026-10-04 09:00". Returns unix ms or null. */
function parseWhen(str, base = now()) {
  let s = String(str || '').trim().replace(/：/g, ':').replace(/\s+/g, ' ');
  if (!s) return null;
  const dur = s.replace(/\s/g, '');
  const durRe = /(\d+(?:\.\d+)?)(天|d|小时|时|h|分钟|分|min|m)/gi;
  if (/^((\d+(?:\.\d+)?)(天|d|小时|时|h|分钟|分|min|m))+后?$/i.test(dur)) {
    let total = 0, m;
    while ((m = durRe.exec(dur))) {
      const v = parseFloat(m[1]), u = m[2].toLowerCase();
      total += v * (u === '天' || u === 'd' ? 86400e3 : u === '小时' || u === '时' || u === 'h' ? 3600e3 : 60e3);
    }
    return base + total;
  }
  let dayOffset = null;
  const pre = s.match(/^(今天|明天|后天)\s*/);
  if (pre) { dayOffset = { 今天: 0, 明天: 1, 后天: 2 }[pre[1]]; s = s.slice(pre[0].length); }
  let m = s.match(/^(\d{1,2}):(\d{2})$/);
  if (m) {
    const d = new Date(base);
    d.setHours(+m[1], +m[2], 0, 0);
    if (dayOffset !== null) d.setDate(d.getDate() + dayOffset);
    else if (d.getTime() <= base) d.setDate(d.getDate() + 1);
    return d.getTime();
  }
  m = s.match(/^(\d{1,2})[-/.月](\d{1,2})日?\s*(\d{1,2}):(\d{2})$/);
  if (m) {
    const b = new Date(base);
    const d = new Date(b.getFullYear(), +m[1] - 1, +m[2], +m[3], +m[4]);
    if (d.getTime() < base - 180 * 86400e3) d.setFullYear(d.getFullYear() + 1);
    return d.getTime();
  }
  m = s.match(/^(\d{4})[-/.年](\d{1,2})[-/.月](\d{1,2})日?[ T]?(\d{1,2}):(\d{2})$/);
  if (m) return new Date(+m[1], +m[2] - 1, +m[3], +m[4], +m[5]).getTime();
  const t = Date.parse(s);
  return isNaN(t) ? null : t;
}

/* ================= quota model ================= */
function isStale(u) { return !!u && now() - u.readAt > (S.settings.staleHours || 6) * 3600e3; }
// effective remaining right now: a window whose reset has passed is assumed full
function effLeft(w) {
  if (!w || w.left == null) return null;
  if (w.reset && w.reset <= now()) return 100;
  return w.left;
}
function nextWeekly(w) {
  if (!w || !w.reset) return null;
  let r = w.reset;
  while (r <= now()) r += 7 * 86400e3;
  return r;
}
function score(a) {
  const u = a.usage;
  if (!u) return null;
  const wk = effLeft(u.weekly);
  const fh = u.noFiveHour ? null : effLeft(u.fiveHour);
  const vals = [wk, fh].filter(v => v != null);
  if (!vals.length) return null;
  return Math.min(...vals);
}
function soonestReset(a) {
  const u = a.usage; if (!u) return Infinity;
  const r = [u.fiveHour?.reset, u.weekly?.reset].filter(x => x && x > now());
  return r.length ? Math.min(...r) : Infinity;
}
function isOnline(a) { return !!S.running[a.id] || a.status === 'online'; }
function displayName(a) { return a.agent ? `${a.agent} · ${a.name}` : a.name; }

/* ================= rendering ================= */
function route() {
  const h = location.hash.replace(/^#\/?/, '');
  const [view, id] = h.split('/');
  return { view: view || 'usage', id };
}

function render() {
  renderSidebar();
  const r = route();
  const main = $('#main');
  const keepScroll = main.scrollTop;
  if (r.view === 'decisions') main.innerHTML = viewDecisions();
  else if (r.view === 'account' && S.accounts.find(a => a.id === r.id)) main.innerHTML = viewAccount(S.accounts.find(a => a.id === r.id));
  else main.innerHTML = viewUsage();
  main.scrollTop = keepScroll;
  bindMain();
}

function renderSidebar() {
  const r = route();
  $$('.nav a').forEach(a => a.classList.toggle('active', a.dataset.nav === r.view));
  const open = S.decisions.filter(d => !d.done).length;
  $('#decCount').textContent = open || '';
  const online = S.accounts.filter(isOnline).length;
  $('#groupTitle').textContent = `账号 · ${online} 在线`;
  $('#groupHead').classList.toggle('collapsed', !ui.groupOpen);
  const q = ui.search.trim().toLowerCase();
  const list = S.accounts.filter(a => !q || [a.name, a.agent, a.plan, a.note, PROVIDERS[a.provider]].join(' ').toLowerCase().includes(q));
  const ul = $('#agentList');
  ul.hidden = !ui.groupOpen && !q;
  if (!list.length) {
    ul.innerHTML = `<li class="empty">${q ? '没有匹配的账号' : '还没有账号，点下方「新账号」添加'}</li>`;
    return;
  }
  ul.innerHTML = list.map(a => {
    const run = S.running[a.id];
    const on = isOnline(a);
    const st = run ? '运行中' : STATUS[a.status] || STATUS.unlinked;
    const sc = score(a);
    const low = sc != null && sc < (S.settings.lowPercent || 15);
    return `<li><a href="#/account/${a.id}" class="${r.view === 'account' && r.id === a.id ? 'active' : ''}">
      <i class="acc-dot" style="background:${esc(a.color || '#999')}"></i>
      <div class="meta"><div class="title">${esc(displayName(a))}</div>
      <div class="status ${on ? 'on' : ''}"><i class="sdot ${run ? 'run' : on ? 'on' : ''}"></i>${st}</div></div>
      ${sc != null ? `<span class="side-pct ${low ? 'low' : ''}" title="当前可用（取 5 小时与每周的较小值）">${Math.round(sc)}%</span>` : ''}
      <svg class="chev" viewBox="0 0 24 24"><path d="m9 6 6 6-6 6"/></svg></a></li>`;
  }).join('');
}

function topbar(title, right = '', left = '') {
  return `<div class="topbar"><div class="left">${left}</div><h2>${title}</h2><div class="right">${right}</div></div>`;
}

const ICON_REFRESH = '<svg viewBox="0 0 24 24"><path d="M20 11a8 8 0 1 0-2.3 5.7"/><path d="M20 4v7h-7"/></svg>';

function viewUsage() {
  let list = S.accounts.filter(a => ui.filter === 'all' || a.provider === ui.filter || (ui.filter === 'other' && !PROVIDERS[a.provider]));
  if (ui.sort === 'left') list = [...list].sort((a, b) => (score(b) ?? -1) - (score(a) ?? -1));
  if (ui.sort === 'reset') list = [...list].sort((a, b) => soonestReset(a) - soonestReset(b));

  const ranked = S.accounts.filter(a => score(a) != null).sort((a, b) => score(b) - score(a));
  const best = ranked[0];
  let reco = '';
  if (best) {
    const u = best.usage;
    const parts = [];
    if (!u.noFiveHour && u.fiveHour?.left != null) parts.push(`5小时${u.fiveHour.reset && u.fiveHour.reset <= now() ? '可能已恢复' : ` ${Math.round(u.fiveHour.left)}%`}`);
    if (u.weekly?.left != null) parts.push(`每周 ${Math.round(effLeft(u.weekly))}%`);
    reco = `<div class="reco" data-go="${best.id}"><span class="dot" style="background:${esc(best.color)}"></span>建议下一个用 <b>${esc(displayName(best))}</b><span class="hint">${parts.join(' · ')}</span></div>`;
  }
  const chip = (k, label, kind) => `<button class="chip ${ui[kind] === k ? 'on' : ''}" data-${kind}="${k}">${label}</button>`;
  const head = topbar('用量与额度',
    `<button class="icon-btn" id="scanBtn" title="刷新（同时读取 Codex 会话文件）">${ICON_REFRESH}</button>`);
  if (!S.accounts.length) {
    return `<div class="view">${head}<div class="empty-state"><h3>还没有账号</h3><p>添加你的 Claude / Codex 等账号，记录每个账号的 5 小时与每周额度。</p><button class="btn accent" data-new>＋ 新账号</button></div></div>`;
  }
  return `<div class="view">${head}
    <div class="summary">
      <div class="label">剩余额度 · 共 ${S.accounts.length} 个账号</div>
      ${reco}
      <div class="chips">
        ${chip('all', '全部', 'filter')}${chip('claude', 'Claude', 'filter')}${chip('codex', 'Codex', 'filter')}${chip('other', '其他', 'filter')}
        <span class="chip-sep"></span>
        ${chip('order', '默认顺序', 'sort')}${chip('left', '剩余最多', 'sort')}${chip('reset', '最快重置', 'sort')}
      </div>
    </div>
    <div class="cards">${list.map(a => usageCard(a, true)).join('') || '<div class="hint">这个分类下没有账号</div>'}</div>
  </div>`;
}

function sourceLine(a) {
  const u = a.usage;
  const stale = isStale(u);
  const src = { report: '会话上报', statusline: '会话上报', file: '会话文件', manual: '手动录入' }[u.source] || '读数';
  if (!stale) return `${src} ${fmtHM(u.readAt)}${u.detail && u.source === 'report' ? ` · ${esc(u.detail)}` : ''}`;
  if (u.source === 'file' || u.source === 'statusline') return `本机 ${relBare(u.readAt)}没用过这个账号，读数停在 ${fmtRead(u.readAt)}；之后网页或别的设备上用的额度这里看不到`;
  return `读数 ${rel(u.readAt)}（${src} ${fmtShortDate(u.readAt)}）`;
}

function windowRow(label, w, opts) {
  const { stale, noWindow, weekly } = opts;
  if (noWindow) {
    return `<div class="win"><span class="wl">${label}</span><span></span><span class="wr dash">—</span>
      <div class="note grey"><span>这个套餐现在没有 5 小时窗口</span></div></div>`;
  }
  if (!w || w.left == null) {
    return `<div class="win"><span class="wl">${label}</span><span></span><span class="wr dash">—</span>
      <div class="note grey"><span>暂无数据</span></div></div>`;
  }
  const left = Math.round(w.left);
  const passed = w.reset && w.reset <= now();
  if (passed) {
    const nxt = weekly ? nextWeekly(w) : null;
    return `<div class="win"><span class="wl">${label}</span><span></span><span class="wr old">重置前剩 ${left}%</span>
      <div class="note green"><span>可能已恢复（未验证）</span></div>
      ${nxt ? `<div class="note grey"><span>约 ${fmtDate(nxt)} 重置</span><span class="cd" data-cd="${nxt}">还有 ${countdown(nxt)}</span></div>` : ''}</div>`;
  }
  const low = w.left < (S.settings.lowPercent || 15);
  const cls = low ? 'low' : stale ? 'muted' : '';
  return `<div class="win"><span class="wl">${label}</span>
    <div class="bar ${cls}"><i style="width:${Math.max(0, Math.min(100, w.left))}%"></i></div>
    <span class="wr ${stale || low ? 'old' : ''}">剩 ${left}%</span>
    ${w.reset ? `<div class="note"><span>${w.approx ? '约 ' : ''}${fmtDate(w.reset)} 重置</span><span class="cd" data-cd="${w.reset}">还有 ${countdown(w.reset)}</span></div>` : '<div class="note grey"><span>重置时间未知</span></div>'}
  </div>`;
}

function usageCard(a, clickable) {
  const u = a.usage;
  const stale = isStale(u);
  const head = `<div class="card-head"><span class="dot" style="background:${esc(a.color || '#999')}"></span>
    <h3>${esc(a.name)}${a.plan ? `<span class="plan">${esc(a.plan)}</span>` : ''}</h3>
    ${stale ? '<span class="badge-old" title="读数已超过设置的时长">旧</span>' : ''}</div>`;
  const acts = `<div class="acts"><button class="mini-btn" data-usage="${a.id}">录入读数</button></div>`;
  if (!u) {
    return `<div class="card ${clickable ? 'click' : ''}" data-go="${clickable ? a.id : ''}">${head}
      <div class="card-empty"><span>还没有读数</span><button class="mini-btn" data-usage="${a.id}">录入读数</button></div>
      ${a.provider === 'codex' && a.autoScan ? '<div class="hint">已开启自动读取 Codex 会话文件，用过一次 codex 后会自动出现</div>' : ''}
      ${a.provider === 'claude' && a.autoScan ? '<div class="hint">已开启自动读数：用这个账号的 Claude Code 发一条消息后会自动出现</div>' : ''}
      ${a.provider === 'claude' && !a.autoScan && !clickable ? `<div class="hint">可以在下方「账号信息」里开启自动读数</div>` : ''}</div>`;
  }
  return `<div class="card ${clickable ? 'click' : ''}" data-go="${clickable ? a.id : ''}">${head}
    <div class="card-sub">${sourceLine(a)}</div>
    ${windowRow('5小时', u.fiveHour, { stale, noWindow: u.noFiveHour })}
    ${windowRow('每周', u.weekly, { stale, weekly: true })}
    <div class="card-foot"><span>读数 ${fmtRead(u.readAt)}（${rel(u.readAt)}${stale ? ' · 旧' : ''}）</span>${acts}</div>
  </div>`;
}

function viewAccount(a) {
  const run = S.running[a.id];
  const on = isOnline(a);
  const idx = S.accounts.indexOf(a);
  const envName = a.provider === 'claude' ? 'CLAUDE_CONFIG_DIR' : a.provider === 'codex' ? 'CODEX_HOME' : 'AGENT_CONFIG_DIR';
  const cfg = a.configDir || (a.provider === 'codex' ? `${S.codexHome}（默认）` : a.provider === 'claude' ? `${S.claudeHome}（默认）` : '（默认，未隔离）');
  const launch = a.launchCmd || ({ claude: 'claude', codex: 'codex' }[a.provider] || '');
  const decs = S.decisions.filter(d => d.account === a.id && !d.done);
  const scanErr = S.scanErrs?.[a.id];
  return `<div class="view">
    ${topbar('账号详情', '', `<a class="btn" href="#/usage">← 用量与额度</a>`)}
    <div class="detail-head">
      <span class="dot" style="background:${esc(a.color)}"></span>
      <h2>${esc(displayName(a))}</h2>
      <span class="plan">${esc(PROVIDERS[a.provider] || a.provider)}${a.plan ? ' · ' + esc(a.plan) : ''}</span>
      <span class="pill ${on ? 'on' : ''}"><i class="sdot ${run ? 'run' : on ? 'on' : ''}"></i>${run ? '运行中' : STATUS[a.status] || ''}</span>
      <div class="btn-row">
        <button class="btn dark" data-launch="${a.id}" ${launch ? '' : 'disabled'} title="${launch ? '用这个账号的配置目录打开一个终端并运行：' + esc(launch) : '先在编辑里填写启动命令'}">
          <svg viewBox="0 0 24 24"><path d="m6 4 14 8-14 8z"/></svg>启动</button>
        <button class="btn" data-opendir="${a.id}"><svg viewBox="0 0 24 24"><path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/></svg>配置目录</button>
        <button class="btn" data-usage="${a.id}">录入读数</button>
        <button class="btn" data-edit="${a.id}">编辑</button>
        <button class="btn" data-move="-1" ${idx <= 0 ? 'disabled' : ''} title="上移">↑</button>
        <button class="btn" data-move="1" ${idx >= S.accounts.length - 1 ? 'disabled' : ''} title="下移">↓</button>
      </div>
    </div>
    <div class="grid2">
      <div style="display:flex;flex-direction:column;gap:18px">
        ${usageCard(a, false)}
        <div class="panel"><h4>账号信息</h4>
          <dl class="kv">
            <dt>类型</dt><dd>${esc(PROVIDERS[a.provider] || a.provider)}</dd>
            <dt>配置目录</dt><dd><code>${esc(cfg)}</code><div class="hint">启动时设置 ${envName}，每个账号用独立目录即可同时登录多个账号</div></dd>
            <dt>启动命令</dt><dd>${launch ? `<code>${esc(launch)}</code>` : '<span class="hint">未设置</span>'}</dd>
            <dt>工作目录</dt><dd>${a.workDir ? `<code>${esc(a.workDir)}</code>` : '<span class="hint">用户主目录</span>'}</dd>
            ${autoReadRow(a, scanErr)}
          </dl>
          ${a.note ? `<h4 style="margin-top:16px">备注</h4><div class="note-text">${esc(a.note)}</div>` : ''}
        </div>
      </div>
      <div style="display:flex;flex-direction:column;gap:18px">
        <div class="panel"><h4>留言 <span class="hint">${a.messages.length} 条</span></h4>
          ${a.messages.length ? `<ul class="msgs">${a.messages.slice().reverse().map(m => `<li>${esc(m.text)}<div class="t">${fmtDate(m.at)} · ${rel(m.at)}</div><button class="x" data-delmsg="${m.id}" title="删除">✕</button></li>`).join('')}</ul>` : `<div class="hint">${on ? '给这个账号记点东西：交接事项、上下文、待办…' : '账号离线时可以先留言，回来再看'}</div>`}
          <div class="compose"><textarea id="msgInput" rows="2" placeholder="写留言，Ctrl+Enter 发送"></textarea><button class="btn dark" id="msgSend">发送</button></div>
        </div>
        <div class="panel"><h4>待拍板 <a class="hint" href="#/decisions">全部 →</a></h4>
          ${decs.length ? `<ul class="dec-list">${decs.map(decItem).join('')}</ul>` : '<div class="hint">没有和这个账号相关的待定事项</div>'}
        </div>
      </div>
    </div>
  </div>`;
}

function autoReadRow(a, scanErr) {
  if (a.provider !== 'codex' && a.provider !== 'claude') return '';
  const sl = S.statusLines?.[a.id];
  let desc;
  if (a.provider === 'codex') {
    desc = a.autoScan ? '已开启：定时读取 Codex 会话文件里的额度' : '未开启';
  } else if (!a.autoScan) {
    desc = sl === 'other'
      ? '未开启。开启后会接管这个账号的状态栏，你原来的状态栏会继续显示'
      : '未开启。开启后 Claude Code 每次刷新状态栏都会把额度报给 Agent Hub';
  } else if (sl === 'on') {
    desc = '已开启：通过 Claude Code 状态栏读取（settings.json → statusLine）';
  } else {
    desc = `<span class="warn">已开启，但 settings.json 里的 statusLine 被改掉了${sl === 'error' ? '（文件读取失败）' : ''}，点「重新开启」修复</span>`;
  }
  const broken = a.provider === 'claude' && a.autoScan && sl !== 'on';
  const btn = `<button class="mini-btn" data-autoread="${a.autoScan && !broken ? 0 : 1}">${broken ? '重新开启' : a.autoScan ? '关闭' : '开启'}</button>`;
  return `<dt>自动读数</dt><dd><div style="display:flex;gap:10px;align-items:flex-start;justify-content:space-between"><span>${desc}</span>${btn}</div>
    ${scanErr && a.autoScan && !broken ? `<div class="hint" style="margin-top:4px">${esc(scanErr)}</div>` : ''}</dd>`;
}

function decItem(d) {
  const a = S.accounts.find(x => x.id === d.account);
  return `<li class="${d.done ? 'done' : ''}">
    <button class="chk" data-toggle="${d.id}" title="${d.done ? '标记为未完成' : '拍板'}">${d.done ? '<svg viewBox="0 0 24 24"><path d="m5 12.5 4.5 4.5L19 7.5"/></svg>' : ''}</button>
    <div class="body"><div class="dt">${esc(d.text)}</div>
      <div class="dm">${a ? `<span class="tag"><i style="background:${esc(a.color)}"></i>${esc(displayName(a))}</span>` : ''}<span>${d.done && d.doneAt ? '拍板于 ' + fmtDate(d.doneAt) : '记于 ' + fmtDate(d.createdAt)}</span></div></div>
    <button class="del" data-deldec="${d.id}" title="删除">✕</button></li>`;
}

function viewDecisions() {
  const open = S.decisions.filter(d => !d.done).sort((a, b) => b.createdAt - a.createdAt);
  const done = S.decisions.filter(d => d.done).sort((a, b) => (b.doneAt || 0) - (a.doneAt || 0));
  return `<div class="view" style="max-width:860px">
    ${topbar('待拍板')}
    <div class="dec-add">
      <input id="decInput" placeholder="有什么要拍板的？回车添加" autocomplete="off">
      <select id="decAcc"><option value="">不关联账号</option>${S.accounts.map(a => `<option value="${a.id}">${esc(displayName(a))}</option>`).join('')}</select>
      <button class="btn dark" id="decAdd">添加</button>
    </div>
    ${open.length ? `<ul class="dec-list">${open.map(decItem).join('')}</ul>` : '<div class="empty-state" style="padding:40px"><p>没有待拍板的事 🎉</p></div>'}
    ${done.length ? `<details class="done-group"><summary>已拍板（${done.length}）</summary><ul class="dec-list">${done.map(decItem).join('')}</ul></details>` : ''}
  </div>`;
}

/* ================= events ================= */
function bindMain() {
  const main = $('#main');
  $$('[data-filter]', main).forEach(b => b.onclick = () => { ui.filter = b.dataset.filter; localStorage.setItem('ah.filter', ui.filter); render(); });
  $$('[data-sort]', main).forEach(b => b.onclick = () => { ui.sort = b.dataset.sort; localStorage.setItem('ah.sort', ui.sort); render(); });
  $$('[data-go]', main).forEach(el => {
    if (!el.dataset.go) return;
    el.onclick = e => { if (e.target.closest('button,a')) return; location.hash = `#/account/${el.dataset.go}`; };
  });
  $$('[data-usage]', main).forEach(b => b.onclick = e => { e.stopPropagation(); usageDialog(S.accounts.find(a => a.id === b.dataset.usage)); });
  $$('[data-edit]', main).forEach(b => b.onclick = () => accountDialog(S.accounts.find(a => a.id === b.dataset.edit)));
  $$('[data-new]', main).forEach(b => b.onclick = () => accountDialog(null));
  $$('[data-launch]', main).forEach(b => b.onclick = () => act(() => api('POST', `/api/accounts/${b.dataset.launch}/launch`), '已在新窗口启动'));
  $$('[data-opendir]', main).forEach(b => b.onclick = () => act(() => api('POST', `/api/accounts/${b.dataset.opendir}/opendir`)));
  const r = route();
  $$('[data-autoread]', main).forEach(b => b.onclick = () => act(async () => {
    await api('POST', `/api/accounts/${r.id}/autoread`, { enable: b.dataset.autoread === '1' });
    if (b.dataset.autoread === '1') setTimeout(refresh, 1500);
  }, b.dataset.autoread === '1' ? '已开启自动读数' : '已关闭自动读数'));
  $$('[data-move]', main).forEach(b => b.onclick = () => act(() => api('POST', `/api/accounts/${r.id}/move`, { dir: +b.dataset.move })));
  $$('[data-delmsg]', main).forEach(b => b.onclick = () => act(() => api('DELETE', `/api/accounts/${r.id}/messages/${b.dataset.delmsg}`)));
  $$('[data-toggle]', main).forEach(b => b.onclick = () => {
    const d = S.decisions.find(x => x.id === b.dataset.toggle);
    act(() => api('PUT', `/api/decisions/${d.id}`, { ...d, done: !d.done }), d.done ? null : '已拍板');
  });
  $$('[data-deldec]', main).forEach(b => b.onclick = () => act(() => api('DELETE', `/api/decisions/${b.dataset.deldec}`)));

  const scan = $('#scanBtn');
  if (scan) scan.onclick = async () => {
    scan.classList.add('spin');
    try {
      const j = await api('POST', '/api/scan');
      toast(j.updated ? `已更新 ${j.updated} 个账号的读数` : '已刷新');
    } catch (e) { toast(e.message, true); }
    finally { setTimeout(() => $('#scanBtn')?.classList.remove('spin'), 400); }
  };

  const send = $('#msgSend');
  if (send) {
    const input = $('#msgInput');
    const go = () => {
      const text = input.value.trim();
      if (!text) return;
      act(async () => { await api('POST', `/api/accounts/${r.id}/messages`, { text }); setTimeout(() => $('#msgInput')?.focus()); });
    };
    send.onclick = go;
    input.onkeydown = e => { if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) go(); };
  }

  const decAdd = $('#decAdd');
  if (decAdd) {
    const input = $('#decInput');
    const go = () => {
      const text = input.value.trim();
      if (!text) return;
      const account = $('#decAcc').value;
      act(async () => { await api('POST', '/api/decisions', { text, account }); setTimeout(() => $('#decInput')?.focus()); });
    };
    decAdd.onclick = go;
    input.onkeydown = e => { if (e.key === 'Enter') go(); };
  }
}

/* ================= dialogs ================= */
const dlg = $('#dlg');
function openDialog(title, body, foot) {
  dlg.innerHTML = `<form method="dialog" class="dlg-wrap" novalidate>
    <div class="dlg-head"><button type="button" class="icon-btn" data-close title="关闭"><svg viewBox="0 0 24 24"><path d="M6 6l12 12M18 6 6 18"/></svg></button><h3>${title}</h3><span style="width:40px"></span></div>
    <div class="dlg-body">${body}</div>
    <div class="dlg-foot">${foot}</div></form>`;
  $$('[data-close]', dlg).forEach(b => b.onclick = () => dlg.close());
  dlg.showModal();
  return dlg;
}

function accountDialog(a) {
  const isNew = !a;
  a = a || { name: '', agent: '', provider: 'claude', plan: 'Max 20x', color: PALETTE[S.accounts.length % PALETTE.length], status: 'online', note: '', configDir: '', launchCmd: '', workDir: '', autoScan: true };
  const body = `<div class="form">
    <label class="l">账号名称</label><input name="name" value="${esc(a.name)}" placeholder="例如 Claude 1" required>
    <label class="l">Agent 名字</label><input name="agent" value="${esc(a.agent)}" placeholder="可选，例如 阿璃（管家）">
    <label class="l">类型</label><div class="row">
      <select name="provider">${Object.entries(PROVIDERS).map(([k, v]) => `<option value="${k}" ${a.provider === k ? 'selected' : ''}>${v}</option>`).join('')}</select>
      <input name="plan" value="${esc(a.plan)}" placeholder="套餐，例如 Max 20x / Pro" list="plans">
      <datalist id="plans"><option>Max 20x</option><option>Max 5x</option><option>Pro</option><option>Plus</option><option>Team</option><option>Business</option><option>API</option></datalist>
    </div>
    <label class="l">颜色</label><div class="swatches">${PALETTE.map(c => `<button type="button" class="swatch ${c === a.color ? 'on' : ''}" data-c="${c}" style="background:${c}"></button>`).join('')}<input type="color" name="color" value="${esc(a.color || '#e2623b')}"></div>
    <label class="l">状态</label><select name="status">${Object.entries(STATUS).map(([k, v]) => `<option value="${k}" ${a.status === k ? 'selected' : ''}>${v}</option>`).join('')}</select>
    <h5>启动与隔离</h5>
    <label class="l">配置目录</label><input name="configDir" value="${esc(a.configDir)}" placeholder="例如 D:\\agents\\claude1（空 = 默认目录）">
    <div></div><div class="hint" style="margin-top:-6px">Claude 用 CLAUDE_CONFIG_DIR，Codex 用 CODEX_HOME。每个账号一个目录，在里面分别登录即可多号并存。</div>
    <label class="l">启动命令</label><input name="launchCmd" value="${esc(a.launchCmd)}" placeholder="空 = claude / codex">
    <label class="l">工作目录</label><input name="workDir" value="${esc(a.workDir)}" placeholder="终端打开的位置（空 = 用户主目录）">
    <label class="l"></label><label class="check" id="autoScanRow"><input type="checkbox" name="autoScan" ${a.autoScan ? 'checked' : ''}> <span id="autoScanText"></span></label>
    <label class="l">备注</label><textarea name="note" rows="3" placeholder="邮箱、到期日、用途…">${esc(a.note)}</textarea>
  </div>`;
  const foot = `${isNew ? '' : '<button type="button" class="btn danger" id="delAcc">删除账号</button>'}<span class="grow"></span>
    <button type="button" class="btn" data-close>取消</button><button type="submit" class="btn dark">${isNew ? '添加' : '保存'}</button>`;
  openDialog(isNew ? '新账号' : '编辑账号', body, foot);
  const f = $('form', dlg);
  const syncProvider = () => {
    const p = f.provider.value;
    $('#autoScanRow').style.display = p === 'codex' || p === 'claude' ? '' : 'none';
    $('#autoScanText').textContent = p === 'codex' ? '自动读数：读取 Codex 会话文件里的额度（sessions/*.jsonl）' : '自动读数：通过 Claude Code 状态栏读取额度（会写入配置目录的 settings.json）';
  };
  f.provider.onchange = () => {
    syncProvider();
    if (isNew && f.provider.value === 'codex') { f.autoScan.checked = true; if (f.plan.value === 'Max 20x') f.plan.value = 'Pro'; }
    if (isNew && f.provider.value === 'claude' && f.plan.value === 'Pro') f.plan.value = 'Max 20x';
  };
  syncProvider();
  $$('.swatch', dlg).forEach(s => s.onclick = () => { f.color.value = s.dataset.c; $$('.swatch', dlg).forEach(x => x.classList.toggle('on', x === s)); });
  f.color.oninput = () => $$('.swatch', dlg).forEach(x => x.classList.remove('on'));
  f.onsubmit = e => {
    e.preventDefault();
    const v = {
      name: f.name.value.trim(), agent: f.agent.value.trim(), provider: f.provider.value, plan: f.plan.value.trim(),
      color: f.color.value, status: f.status.value, configDir: f.configDir.value.trim(), launchCmd: f.launchCmd.value.trim(),
      workDir: f.workDir.value.trim(), autoScan: (f.provider.value === 'codex' || f.provider.value === 'claude') && f.autoScan.checked, note: f.note.value,
    };
    if (!v.name) { f.name.focus(); toast('请填写账号名称', true); return; }
    act(async () => {
      if (isNew) {
        const before = new Set(S.accounts.map(x => x.id));
        await api('POST', '/api/accounts', v);
        const created = S.accounts.find(x => !before.has(x.id));
        if (created) location.hash = `#/account/${created.id}`;
      } else {
        await api('PUT', `/api/accounts/${a.id}`, v);
      }
      dlg.close();
    }, isNew ? '已添加' : '已保存');
  };
  const del = $('#delAcc');
  if (del) del.onclick = () => {
    if (!confirm(`确定删除「${displayName(a)}」？读数和留言都会删除。`)) return;
    act(async () => { await api('DELETE', `/api/accounts/${a.id}`); dlg.close(); location.hash = '#/usage'; }, '已删除');
  };
}

function usageDialog(a) {
  const u = a.usage || {};
  const fh = u.fiveHour || {}, wk = u.weekly || {};
  const fmtIn = ms => ms ? fmtShortDate(ms) : '';
  const winFields = (key, w, label) => `
    <h5>${label}</h5>
    <label class="l">额度</label><div class="row">
      <input name="${key}Val" type="number" min="0" max="100" step="0.1" value="${w.left ?? ''}" placeholder="百分比">
      <span class="seg" data-seg="${key}"><button type="button" class="on" data-m="left">剩余 %</button><button type="button" data-m="used">已用 %</button></span>
    </div>
    <label class="l">重置时间</label><input name="${key}Reset" value="${esc(fmtIn(w.reset))}" placeholder="例如 2h30m · 09:00 · 明天 9:00 · 10-04 09:00">
    <div class="preview" data-preview="${key}"></div>`;
  const body = `<div class="form">
    <div class="full hint">在 Claude / ChatGPT 的用量页面或 CLI（/usage、/status）里看到额度后，填到这里。重置时间可以写「多久后」或具体时间。</div>
    ${winFields('fh', fh, '5 小时窗口')}
    <label class="l"></label><label class="check"><input type="checkbox" name="noFive" ${u.noFiveHour ? 'checked' : ''}> 这个套餐现在没有 5 小时窗口</label>
    ${winFields('wk', wk, '每周')}
    <label class="l"></label><label class="check"><input type="checkbox" name="wkApprox" ${wk.approx ? 'checked' : ''}> 重置时间是估计的（显示「约」）</label>
    <h5>读数</h5>
    <label class="l">读数时间</label><input name="readAt" placeholder="空 = 现在；也可以写 10-04 08:30">
    <div class="preview" data-preview="readAt"></div>
  </div>`;
  const foot = `${a.usage ? '<button type="button" class="btn danger" id="clrUsage">清除读数</button>' : ''}<span class="grow"></span>
    <button type="button" class="btn" data-close>取消</button><button type="submit" class="btn dark">保存</button>`;
  openDialog(`录入读数 · ${esc(a.name)}`, body, foot);
  const f = $('form', dlg);
  const mode = { fh: 'left', wk: 'left' };
  $$('[data-seg]', dlg).forEach(seg => $$('button', seg).forEach(b => b.onclick = () => {
    const k = seg.dataset.seg;
    if (mode[k] !== b.dataset.m && f[k + 'Val'].value !== '') f[k + 'Val'].value = +(100 - f[k + 'Val'].value).toFixed(1);
    mode[k] = b.dataset.m;
    $$('button', seg).forEach(x => x.classList.toggle('on', x === b));
  }));
  const preview = (name, key, future) => {
    const el = $(`[data-preview="${key}"]`, dlg);
    const v = f[name].value.trim();
    if (!v) { el.textContent = ''; el.classList.remove('bad'); return; }
    const t = parseWhen(v);
    if (t == null) { el.textContent = '无法识别这个时间'; el.classList.add('bad'); return; }
    el.classList.remove('bad');
    const diff = t - now();
    el.textContent = `→ ${fmtDate(t)}（${future ? (diff > 0 ? '还有 ' + humanDur(diff) : '已经过去了') : diff > 60e3 ? '在未来？' : relBare(t) + '前'}）`;
  };
  const sync = () => {
    preview('fhReset', 'fh', true); preview('wkReset', 'wk', true); preview('readAt', 'readAt', false);
    const no = f.noFive.checked;
    f.fhVal.disabled = no; f.fhReset.disabled = no;
  };
  ['fhReset', 'wkReset', 'readAt'].forEach(n => f[n].oninput = sync);
  f.noFive.onchange = sync;
  sync();
  const win = key => {
    const raw = f[key + 'Val'].value.trim();
    const resetStr = f[key + 'Reset'].value.trim();
    if (raw === '' && !resetStr) return null;
    let left = raw === '' ? null : Math.max(0, Math.min(100, +raw));
    if (left != null && mode[key] === 'used') left = +(100 - left).toFixed(1);
    const reset = resetStr ? parseWhen(resetStr) : null;
    if (resetStr && reset == null) throw new Error(`${key === 'fh' ? '5 小时' : '每周'}的重置时间无法识别`);
    return { left, reset: reset || 0 };
  };
  f.onsubmit = e => {
    e.preventDefault();
    act(async () => {
      const body = { noFiveHour: f.noFive.checked };
      if (!body.noFiveHour) body.fiveHour = win('fh');
      body.weekly = win('wk');
      if (body.weekly && f.wkApprox.checked) body.weekly.approx = true;
      if (f.readAt.value.trim()) {
        const t = parseWhen(f.readAt.value.trim(), now());
        if (t == null) throw new Error('读数时间无法识别');
        body.readAt = t > now() && /^\d{1,2}:\d{2}$/.test(f.readAt.value.trim()) ? t - 86400e3 : t;
      }
      if (!body.fiveHour && !body.weekly && !body.noFiveHour) throw new Error('至少填一个额度');
      await api('POST', `/api/accounts/${a.id}/usage`, body);
      dlg.close();
    }, '读数已保存');
  };
  const clr = $('#clrUsage');
  if (clr) clr.onclick = () => act(async () => { await api('DELETE', `/api/accounts/${a.id}/usage`); dlg.close(); }, '已清除');
}

function settingsDialog() {
  const st = S.settings;
  const base = `http://127.0.0.1:${S.port}`;
  const ps = `$body = @{ account = "Claude 1"; five_hour = @{ left = 77; resets_in_seconds = 9000 }; weekly = @{ used = 32; resets_in_seconds = 400000 } } | ConvertTo-Json
Invoke-RestMethod -Method Post -Uri "${base}/api/report" -Headers @{ "X-Token" = "${st.token}" } -ContentType "application/json; charset=utf-8" -Body ([Text.Encoding]::UTF8.GetBytes($body))`;
  const curl = `curl -X POST ${base}/api/report -H "X-Token: ${st.token}" -H "Content-Type: application/json" -d "{\\"account\\":\\"Codex 2\\",\\"weekly\\":{\\"left\\":92,\\"reset\\":\\"2026-10-10T06:35:00+08:00\\"},\\"no_five_hour\\":true}"`;
  const body = `<div class="form">
    <h5>显示</h5>
    <label class="l">读数变「旧」</label><div class="row"><input name="staleHours" type="number" min="0.5" step="0.5" value="${st.staleHours}"><span class="hint">小时后</span></div>
    <label class="l">低额度提醒</label><div class="row"><input name="lowPercent" type="number" min="1" max="99" value="${st.lowPercent}"><span class="hint">% 以下进度条变色</span></div>
    <label class="l">自动读取间隔</label><div class="row"><input name="scanSeconds" type="number" min="10" value="${st.scanSeconds}"><span class="hint">秒（Codex 会话文件）</span></div>
    <h5>会话上报 API</h5>
    <div class="full hint">脚本、Hook 或别的工具可以把额度推送过来，卡片上会显示「会话上报」。支持 left / used / utilization / used_percent、reset / resets_at / resets_in_seconds，也接受 Codex 的 primary/secondary 和 Claude 的 five_hour/seven_day 格式。</div>
    <label class="l">Token</label><div class="row"><input readonly value="${esc(st.token)}" id="tokenField"><button type="button" class="btn" id="regen">重新生成</button></div>
    <div class="full"><div class="code"><button type="button" class="copy" data-copy="ps">复制</button>${esc(ps)}</div></div>
    <div class="full"><div class="code"><button type="button" class="copy" data-copy="curl">复制</button>${esc(curl)}</div></div>
    <h5>数据</h5>
    <label class="l">数据文件</label><div class="hint" style="word-break:break-all">${esc(S.dataPath)}<br>把 data.json 放到 exe 同目录即可变成便携版</div>
    <label class="l">备份</label><div class="row"><a class="btn" href="/api/export" download>导出 JSON</a><label class="btn" style="cursor:pointer">导入 JSON<input type="file" accept=".json,application/json" id="importFile" hidden></label></div>
    <label class="l">版本</label><div class="hint">Agent Hub ${esc(S.version)} · 端口 ${S.port}</div>
  </div>`;
  openDialog('设置', body, `<button type="button" class="btn" data-close>关闭</button><button type="submit" class="btn dark">保存</button>`);
  const f = $('form', dlg);
  const texts = { ps, curl };
  $$('[data-copy]', dlg).forEach(b => b.onclick = async () => {
    try { await navigator.clipboard.writeText(texts[b.dataset.copy]); toast('已复制'); } catch { toast('复制失败，请手动选择', true); }
  });
  $('#regen').onclick = () => {
    if (!confirm('重新生成后，旧 token 的上报脚本会失效。继续？')) return;
    act(async () => { await api('PUT', '/api/settings', { regenToken: true }); settingsDialog(); }, '已重新生成');
  };
  $('#importFile').onchange = async e => {
    const file = e.target.files[0];
    if (!file) return;
    if (!confirm('导入会覆盖当前所有数据，确定？')) return;
    act(async () => { await api('POST', '/api/import', JSON.parse(await file.text())); dlg.close(); }, '已导入');
  };
  f.onsubmit = e => {
    e.preventDefault();
    act(async () => {
      await api('PUT', '/api/settings', { staleHours: +f.staleHours.value, lowPercent: +f.lowPercent.value, scanSeconds: +f.scanSeconds.value });
      dlg.close();
    }, '已保存');
  };
}

/* ================= boot ================= */
$('#addBtn').onclick = () => accountDialog(null);
$('#settingsBtn').onclick = settingsDialog;
$('#groupHead').onclick = () => { ui.groupOpen = !ui.groupOpen; localStorage.setItem('ah.group', ui.groupOpen ? '1' : '0'); renderSidebar(); };
$('#searchBtn').onclick = () => {
  const w = $('#searchWrap');
  w.hidden = !w.hidden;
  if (!w.hidden) $('#searchInput').focus(); else { ui.search = ''; $('#searchInput').value = ''; renderSidebar(); }
};
$('#searchInput').oninput = e => { ui.search = e.target.value; renderSidebar(); };
window.addEventListener('hashchange', () => { $('#main').scrollTop = 0; render(); });
document.addEventListener('keydown', e => {
  if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); $('#searchWrap').hidden = false; $('#searchInput').focus(); }
});

// live countdowns; re-render when a reset time is crossed
let lastCrossCheck = 0;
setInterval(() => {
  let crossed = false;
  $$('[data-cd]').forEach(el => {
    const t = +el.dataset.cd;
    if (t <= now()) crossed = true;
    else el.textContent = `还有 ${countdown(t)}`;
  });
  if ((crossed || now() - lastCrossCheck > 60e3) && !dlg.open && !document.activeElement?.matches('input,textarea,select')) {
    lastCrossCheck = now();
    render();
  }
}, 1000);
setInterval(refresh, 15000);
setInterval(() => fetch('/api/ping').catch(() => {}), 20000);
fetch('/api/ping').catch(() => {});
refresh();
