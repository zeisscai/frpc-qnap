// frpc-qnap 前端：原生 ES 模块，无构建步骤。
// 路由用 hash，资源用相对路径，确保在 QTS 反向代理的子路径下也能正常工作。

const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

const PAGES = ['overview', 'proxies', 'settings', 'logs'];

const state = {
  meta: null,
  config: null,
  overview: null,
  proxies: [],
  page: 'overview',
  editingIndex: -1,
  pollTimer: null,
  logTimer: null,
};

// ---------- 基础工具 ----------

function esc(v) {
  return String(v ?? '').replace(/[&<>"']/g, c => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  }[c]));
}

function int(v, def = 0) {
  const n = parseInt(v, 10);
  return Number.isFinite(n) ? n : def;
}

let toastTimer = null;
function toast(msg, kind = '') {
  const el = $('#toast');
  el.textContent = msg;
  el.className = 'toast show ' + kind;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { el.className = 'toast ' + kind; }, 2800);
}

async function api(path, { method = 'GET', body } = {}) {
  const headers = {};
  if (method !== 'GET') {
    headers['Content-Type'] = 'application/json';
    // 与后端的 CSRF 约定：写操作必须带这个自定义头。
    headers['X-Requested-With'] = 'frpc-qnap';
  }
  const res = await fetch(path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: 'same-origin',
  });
  let data = null;
  try { data = await res.json(); } catch { /* 空响应 */ }

  if (res.status === 401) {
    showLogin();
    throw new Error((data && data.error) || '未登录');
  }
  if (!res.ok) {
    throw new Error((data && data.error) || `请求失败（HTTP ${res.status}）`);
  }
  return data;
}

// ---------- 登录 ----------

function showLogin() {
  $('#app').classList.add('hidden');
  $('#login').classList.remove('hidden');
  stopPolling();
  setTimeout(() => $('#login-password').focus(), 60);
}

async function enterApp() {
  $('#login').classList.add('hidden');
  $('#app').classList.remove('hidden');

  state.meta = await api('api/meta');
  await loadConfig();
  fillSelects();
  renderSettings();
  await refreshAll();
  startPolling();
  navigate();
}

async function handleLogin(e) {
  e.preventDefault();
  const pw = $('#login-password').value;
  const hint = $('#login-hint');
  hint.textContent = '';
  try {
    await api('api/login', { method: 'POST', body: { password: pw } });
    $('#login-password').value = '';
    await enterApp();
  } catch (err) {
    hint.textContent = err.message;
  }
}

async function handleLogout() {
  try { await api('api/logout', { method: 'POST', body: {} }); } catch { /* 忽略 */ }
  showLogin();
}

// loadConfig 把 tokenSet 这类只读标记与可提交配置分离，
// 后端使用 DisallowUnknownFields，多带字段会被拒绝。
async function loadConfig() {
  const cfg = await api('api/config');
  state.tokenSet = !!cfg.tokenSet;
  delete cfg.tokenSet;
  state.config = cfg;
  return cfg;
}

// ---------- 路由 ----------

function currentPage() {
  const h = location.hash.replace(/^#\/?/, '');
  return PAGES.includes(h) ? h : 'overview';
}

function navigate() {
  state.page = currentPage();
  $$('.tab').forEach(t => t.classList.toggle('active', t.dataset.tab === state.page));
  $$('.page').forEach(p => p.classList.toggle('active', p.id === 'page-' + state.page));

  if (state.page === 'settings' && state.config) renderSettings();
  if (state.page === 'logs') {
    loadLogs();
    startLogPolling();
  } else {
    stopLogPolling();
  }
}

// ---------- 轮询 ----------

function startPolling() {
  stopPolling();
  state.pollTimer = setInterval(() => {
    if (document.hidden) return;
    if (state.page === 'overview' || state.page === 'proxies') refreshAll().catch(() => {});
  }, 5000);
}

function stopPolling() {
  if (state.pollTimer) clearInterval(state.pollTimer);
  state.pollTimer = null;
}

function startLogPolling() {
  stopLogPolling();
  state.logTimer = setInterval(() => {
    if (document.hidden) return;
    if ($('#log-auto').checked) loadLogs(true);
  }, 10000);
}

function stopLogPolling() {
  if (state.logTimer) clearInterval(state.logTimer);
  state.logTimer = null;
}

// ---------- 概览 / 隧道 ----------

async function refreshAll() {
  const [overview, proxies] = await Promise.all([api('api/overview'), api('api/proxies')]);
  state.overview = overview;
  state.proxies = proxies.proxies || [];
  renderOverview();
  renderProxyTables();
}

function statusText(s) {
  if (!s) return '未知';
  if (s === 'running') return '运行中';
  if (s === 'waiting') return '等待中';
  if (s === 'unknown') return '等待连接';
  // frpc 会返回 "start error" 这类组合状态，统一显示为错误。
  if (s.includes('error')) return '错误';
  return s;
}

function statusClass(s) {
  if (s === 'running' || s === 'waiting') return s;
  if (s && s.includes('error')) return 'error';
  return 'unknown';
}

function renderOverview() {
  const o = state.overview;
  if (!o) return;

  const p = o.process || {};
  const running = !!p.running;
  const cannotConnect = !o.adminOnline && !!o.server.addr;

  $('#brand-dot').className = 'dot ' + (!o.server.addr ? 'warn' : (o.counts.error > 0 ? 'err' : (running ? 'ok' : 'warn')));
  const total = o.counts.total;
  const okCount = o.counts.running;
  const errCount = o.counts.error;

  const badge = $('#conn-badge');
  if (!o.server.addr) {
    badge.textContent = '未配置';
    badge.className = 'badge warn';
  } else if (p.failed) {
    badge.textContent = '进程异常';
    badge.className = 'badge err';
  } else if (!running) {
    badge.textContent = '已停止';
    badge.className = 'badge warn';
  } else if (errCount > 0) {
    badge.textContent = `${errCount} 条隧道异常`;
    badge.className = 'badge err';
  } else if (total > 0 && okCount === 0) {
    badge.textContent = '未连接 frps';
    badge.className = 'badge warn';
  } else {
    badge.textContent = '已连接';
    badge.className = 'badge ok';
  }

  $('#stat-process').textContent = running ? '运行中' : (p.failed ? '已停止（异常）' : '已停止');
  $('#stat-process-sub').textContent = running
    ? `PID ${p.pid}${p.restarts ? ` · 已重启 ${p.restarts} 次` : ''}`
    : (p.lastError || '等待启动');

  $('#stat-proxies').textContent = `${okCount}/${total}`;
  $('#stat-proxies-sub').textContent = total === 0
    ? '还没有隧道'
    : (errCount > 0
      ? `${errCount} 条异常`
      : (okCount === total ? '全部正常' : `${total - okCount} 条等待连接`));

  $('#stat-server').textContent = o.server.addr ? `${o.server.addr}:${o.server.port}` : '未配置';
  $('#stat-server-sub').textContent = cannotConnect ? 'frpc 管理接口不可达' : `协议 ${o.server.protocol}`;

  $('#stat-version').textContent = o.frpcVersion || '—';
  $('#stat-version-sub').textContent = `管理端 ${o.version}`;

  $('#version-label').textContent = `v${o.version}${o.frpcVersion ? ' · frpc ' + o.frpcVersion : ''}`;

  // 概览页表格
  const tb = $('#overview-table tbody');
  if (!state.proxies.length) {
    tb.innerHTML = '<tr><td colspan="5" class="empty">还没有隧道，去「隧道」页添加一条</td></tr>';
    return;
  }
  tb.innerHTML = state.proxies.map(p => `
    <tr>
      <td>${esc(p.name)}</td>
      <td class="mono">${esc((p.type || '').toUpperCase())}</td>
      <td class="mono">${esc(p.localIP || '127.0.0.1')}:${esc(p.localPort)}</td>
      <td class="mono">${esc(p.remoteAddr || (p.remotePort ? ':' + p.remotePort : p.customDomains || p.subdomain || '—'))}</td>
      <td><span class="status ${statusClass(p.status)}">${statusText(p.status)}</span></td>
    </tr>`).join('');
}

function renderProxyTables() {
  const tb = $('#proxies-table tbody');
  if (!state.proxies.length) {
    tb.innerHTML = '<tr><td colspan="6" class="empty">还没有隧道。点右上角「新增隧道」开始。</td></tr>';
    return;
  }
  tb.innerHTML = state.proxies.map((p, i) => `
    <tr>
      <td>
        <div>${esc(p.name)}</div>
        ${p.remark ? `<div class="muted" style="font-size:11px">${esc(p.remark)}</div>` : ''}
      </td>
      <td class="mono">${esc((p.type || '').toUpperCase())}</td>
      <td class="mono">${esc(p.localIP || '127.0.0.1')}:${esc(p.localPort)}</td>
      <td class="mono">${esc(p.remoteAddr || (p.remotePort ? ':' + p.remotePort : p.customDomains || p.subdomain || '—'))}</td>
      <td>
        <span class="status ${statusClass(p.status)}" title="${esc(p.err || '')}">${statusText(p.status)}</span>
        ${p.err ? `<div class="muted" style="font-size:11px">${esc(p.err)}</div>` : ''}
      </td>
      <td class="col-ops">
        <button class="btn ghost sm" data-edit="${i}">编辑</button>
        <button class="btn danger sm" data-del="${i}">删除</button>
      </td>
    </tr>`).join('');
}

// ---------- 设置 ----------

function fillSelects() {
  const m = state.meta;
  const protocol = $('#settings-form').protocol;
  protocol.innerHTML = m.protocols.map(p => `<option value="${esc(p)}">${esc(p.toUpperCase())}</option>`).join('');

  const level = $('#settings-form').logLevel;
  level.innerHTML = m.logLevels.map(l => `<option value="${esc(l)}">${esc(l)}</option>`).join('');

  const type = $('#proxy-type');
  type.innerHTML = m.proxyTypes.map(t => `<option value="${esc(t)}">${esc(t.toUpperCase())}</option>`).join('');
}

function renderSettings() {
  const f = $('#settings-form');
  const c = state.config;
  if (!f || !c) return;

  f.serverAddr.value = c.serverAddr || '';
  f.serverPort.value = c.serverPort || 7000;
  f.authToken.value = '';
  f.authUser.value = c.authUser || '';
  f.protocol.value = c.protocol || 'tcp';
  f.poolCount.value = c.poolCount || 0;
  f.proxyURL.value = c.proxyURL || '';
  f.tlsEnable.checked = !!c.tlsEnable;
  f.tcpMux.checked = !!c.tcpMux;
  f.logLevel.value = c.logLevel || 'info';
  f.logMaxDays.value = c.logMaxDays || 3;
  f.loginFailExit.checked = !!c.loginFailExit;

  $('#token-hint').textContent = state.tokenSet ? '（已设置，留空不修改）' : '（未设置）';
}

async function saveSettings(e) {
  e.preventDefault();
  const f = $('#settings-form');
  const hint = $('#save-hint');
  hint.textContent = '正在保存…';

  const next = {
    ...state.config,
    serverAddr: f.serverAddr.value.trim(),
    serverPort: int(f.serverPort.value, 7000),
    authToken: f.authToken.value,
    authUser: f.authUser.value.trim(),
    protocol: f.protocol.value,
    poolCount: int(f.poolCount.value, 0),
    proxyURL: f.proxyURL.value.trim(),
    tlsEnable: f.tlsEnable.checked,
    tcpMux: f.tcpMux.checked,
    logLevel: f.logLevel.value,
    logMaxDays: int(f.logMaxDays.value, 3),
    loginFailExit: f.loginFailExit.checked,
    proxies: state.config.proxies,
  };

  try {
    const res = await api('api/config', { method: 'PUT', body: next });
    await loadConfig();
    renderSettings();
    await refreshAll();
    hint.textContent = '';
    toast(res.message || '已保存', 'ok');
  } catch (err) {
    hint.textContent = '';
    toast(err.message, 'err');
  }
}

// ---------- 隧道编辑 ----------

function applyFieldVisibility() {
  const f = $('#proxy-form');
  const type = f.type.value;
  const hc = f.healthCheckType.value;
  $$('[data-when]', f).forEach(el => {
    const w = el.dataset.when;
    let show = false;
    if (w === 'tcpudp') show = type === 'tcp' || type === 'udp';
    if (w === 'domain') show = type === 'http' || type === 'https';
    if (w === 'p2p') show = ['stcp', 'xtcp', 'sudp'].includes(type);
    if (w === 'hc-http') show = hc === 'http';
    el.classList.toggle('hidden', !show);
  });
}

function openProxyModal(index) {
  const f = $('#proxy-form');
  f.reset();
  state.editingIndex = index;

  const p = index >= 0 ? state.config.proxies[index] : null;
  $('#modal-title').textContent = p ? `编辑隧道 · ${p.name}` : '新增隧道';

  if (p) {
    f.name.value = p.name || '';
    f.type.value = p.type || 'tcp';
    f.localIP.value = p.localIP || '127.0.0.1';
    f.localPort.value = p.localPort || '';
    f.remotePort.value = p.remotePort || '';
    f.customDomains.value = p.customDomains || '';
    f.subdomain.value = p.subdomain || '';
    f.locations.value = p.locations || '';
    f.hostHeaderRewrite.value = p.hostHeaderRewrite || '';
    f.httpUser.value = p.httpUser || '';
    f.httpPassword.value = '';
    f.secretKey.value = '';
    f.remark.value = p.remark || '';
    f.useEncryption.checked = !!p.useEncryption;
    f.useCompression.checked = !!p.useCompression;
    f.healthCheckType.value = p.healthCheckType || '';
    f.healthCheckURL.value = p.healthCheckURL || '';
    f.healthCheckTimeoutS.value = p.healthCheckTimeoutS || '';
    f.healthCheckMaxFailed.value = p.healthCheckMaxFailed || '';
    f.healthCheckIntervalS.value = p.healthCheckIntervalS || '';
  } else {
    f.localIP.value = '127.0.0.1';
    f.type.value = 'tcp';
  }

  f.name.disabled = !!p; // 名称是密钥继承的键，编辑时不允许改
  applyFieldVisibility();
  $('#modal').classList.remove('hidden');
  setTimeout(() => f.localPort.focus(), 50);
}

function closeProxyModal() {
  $('#modal').classList.add('hidden');
  state.editingIndex = -1;
}

async function saveProxy(e) {
  e.preventDefault();
  const f = $('#proxy-form');

  const proxy = {
    name: f.name.value.trim(),
    type: f.type.value,
    localIP: f.localIP.value.trim() || '127.0.0.1',
    localPort: int(f.localPort.value),
    remotePort: int(f.remotePort.value),
    customDomains: f.customDomains.value.trim(),
    subdomain: f.subdomain.value.trim(),
    locations: f.locations.value.trim(),
    httpUser: f.httpUser.value.trim(),
    httpPassword: f.httpPassword.value,
    hostHeaderRewrite: f.hostHeaderRewrite.value.trim(),
    useEncryption: f.useEncryption.checked,
    useCompression: f.useCompression.checked,
    secretKey: f.secretKey.value,
    healthCheckType: f.healthCheckType.value,
    healthCheckURL: f.healthCheckURL.value.trim(),
    healthCheckTimeoutS: int(f.healthCheckTimeoutS.value),
    healthCheckMaxFailed: int(f.healthCheckMaxFailed.value),
    healthCheckIntervalS: int(f.healthCheckIntervalS.value),
    remark: f.remark.value.trim(),
  };

  const proxies = state.config.proxies.slice();
  if (state.editingIndex >= 0) {
    proxies[state.editingIndex] = proxy;
  } else {
    proxies.push(proxy);
  }

  try {
    const res = await api('api/config', { method: 'PUT', body: { ...state.config, proxies } });
    await loadConfig();
    await refreshAll();
    closeProxyModal();
    toast(res.message || '已保存', 'ok');
  } catch (err) {
    toast(err.message, 'err');
  }
}

async function deleteProxy(index) {
  const p = state.config.proxies[index];
  if (!p) return;
  if (!confirm(`删除隧道「${p.name}」？这会断开它的连接。`)) return;

  const proxies = state.config.proxies.slice();
  proxies.splice(index, 1);
  try {
    const res = await api('api/config', { method: 'PUT', body: { ...state.config, proxies } });
    await loadConfig();
    await refreshAll();
    toast(res.message || '已删除', 'ok');
  } catch (err) {
    toast(err.message, 'err');
  }
}

// ---------- 服务控制 ----------

async function serviceAction(action) {
  try {
    await api(`api/service/${action}`, { method: 'POST', body: {} });
    toast(`已执行：${action}`, 'ok');
    await refreshAll();
  } catch (err) {
    toast(err.message, 'err');
  }
}

async function reloadConfig() {
  try {
    await api('api/reload', { method: 'POST', body: {} });
    toast('已触发热重载', 'ok');
    await refreshAll();
  } catch (err) {
    toast(err.message, 'err');
  }
}

// ---------- 日志 ----------

async function loadLogs(silent = false) {
  const file = $('#log-file').value;
  const lines = $('#log-lines').value;
  const pre = $('#log-content');
  try {
    const res = await api(`api/logs?file=${encodeURIComponent(file)}&lines=${encodeURIComponent(lines)}`);
    const atBottom = pre.scrollTop + pre.clientHeight >= pre.scrollHeight - 24;
    pre.textContent = res.content || '（暂无日志）';
    if (atBottom || !silent) pre.scrollTop = pre.scrollHeight;
  } catch (err) {
    if (!silent) pre.textContent = err.message;
  }
}

// ---------- 事件绑定 ----------

function bindEvents() {
  $('#login-form').addEventListener('submit', handleLogin);
  $('#btn-logout').addEventListener('click', handleLogout);

  $('#settings-form').addEventListener('submit', saveSettings);
  $('#btn-add-proxy').addEventListener('click', () => openProxyModal(-1));
  $('#btn-refresh-proxies').addEventListener('click', () => refreshAll().then(() => toast('已刷新', 'ok')).catch(e => toast(e.message, 'err')));

  $('#proxies-table').addEventListener('click', e => {
    const edit = e.target.closest('[data-edit]');
    if (edit) { openProxyModal(int(edit.dataset.edit, -1)); return; }
    const del = e.target.closest('[data-del]');
    if (del) deleteProxy(int(del.dataset.del, -1));
  });

  $$('[data-svc]').forEach(b => b.addEventListener('click', () => serviceAction(b.dataset.svc)));
  $('#btn-reload').addEventListener('click', reloadConfig);

  $('#modal-close').addEventListener('click', closeProxyModal);
  $('#modal-cancel').addEventListener('click', closeProxyModal);
  $('#proxy-form').addEventListener('submit', saveProxy);
  $('#proxy-type').addEventListener('change', applyFieldVisibility);
  $('#proxy-form').healthCheckType.addEventListener('change', applyFieldVisibility);

  $('#modal').addEventListener('click', e => {
    if (e.target === $('#modal')) closeProxyModal();
  });
  document.addEventListener('keydown', e => {
    if (e.key === 'Escape' && !$('#modal').classList.contains('hidden')) closeProxyModal();
  });

  $('#btn-refresh-logs').addEventListener('click', () => loadLogs());
  $('#log-file').addEventListener('change', () => loadLogs());
  $('#log-lines').addEventListener('change', () => loadLogs());

  window.addEventListener('hashchange', navigate);
}

// ---------- 启动 ----------

async function boot() {
  bindEvents();
  try {
    const s = await api('api/session');
    if (!s.authenticated) {
      showLogin();
      return;
    }
    await enterApp();
  } catch {
    showLogin();
  }
}

boot();
