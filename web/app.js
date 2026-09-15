const TICK = 5;
let cd = TICK;
let timer;
let logsAbortController = null;

const lvlColor = {
  debug: '#64748b',
  info: '#22d3ee',
  warn: '#f59e0b',
  error: '#ef4444',
};

const svcColors = [
  '#3b82f6',
  '#8b5cf6',
  '#06b6d4',
  '#10b981',
  '#f97316',
  '#ec4899',
];

// Полное и безопасное экранирование HTML-сущностей (включая кавычки для атрибутов)
function esc(s) {
  if (s === null || s === undefined) return '';
  return String(s)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

function fmtTs(ts) {
  if (!ts) return '—';
  const d = new Date(ts);
  if (isNaN(d.getTime())) return esc(ts);
  return (
    d.toLocaleDateString('ru-RU', { day: '2-digit', month: '2-digit' }) +
    ' ' +
    d.toLocaleTimeString('ru-RU', { hour12: false }) +
    '.' +
    String(d.getMilliseconds()).padStart(3, '0')
  );
}

function badge(lvl) {
  const safe = esc(lvl || 'info');
  return `<span class="badge lv-${safe}">${safe}</span>`;
}

function renderBars(id, data, colorFn) {
  const el = document.getElementById(id);
  if (!el) return;
  if (!data || !Object.keys(data).length) {
    el.innerHTML = '<div style="color:var(--muted);font-size:13px">Нет данных</div>';
    return;
  }
  const max = Math.max(...Object.values(data));
  const keys = Object.keys(data).sort((a, b) => data[b] - data[a]);
  el.innerHTML = keys
    .map((k, i) => {
      const val = Number(data[k]) || 0;
      const pct = max > 0 ? ((val / max) * 100).toFixed(1) : 0;
      const color = colorFn ? colorFn(k, i) : '#3b82f6';
      return `<div class="bar-row">
      <div class="bar-key" title="${esc(k)}">${esc(k)}</div>
      <div class="bar-track"><div class="bar-fill" style="width:${pct}%;background:${esc(color)}"></div></div>
      <div class="bar-cnt">${val}</div>
    </div>`;
    })
    .join('');
}

async function fetchStats() {
  try {
    const res = await fetch('/api/v1/stats');
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const d = await res.json();
    document.getElementById('s-acc').textContent = d.accepted ?? '—';
    document.getElementById('s-drp').textContent = d.dropped ?? '—';
    document.getElementById('s-err').textContent = d.errors ?? '—';
    document.getElementById('s-errlvl').textContent = d.by_level?.error ?? 0;
    document.getElementById('s-mem').textContent = d.in_memory ?? '—';
    document.getElementById('s-up').textContent = d.uptime ?? '—';
    renderBars('by-svc', d.by_service, (k, i) => svcColors[i % svcColors.length]);
    renderBars('by-lvl', d.by_level, (k) => lvlColor[k] || '#3b82f6');
  } catch (e) {
    console.error('stats error:', e);
  }
}

async function fetchLogs() {
  if (logsAbortController) {
    logsAbortController.abort();
  }
  logsAbortController = new AbortController();

  const p = new URLSearchParams({
    service: document.getElementById('f-svc').value,
    level: document.getElementById('f-lvl').value,
    search: document.getElementById('f-q').value,
    limit: document.getElementById('f-n').value,
  });

  [...p.keys()].forEach((k) => {
    if (!p.get(k)) p.delete(k);
  });

  try {
    const res = await fetch('/api/v1/logs?' + p, { signal: logsAbortController.signal });
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const logs = await res.json();
    document.getElementById('log-cnt').textContent = `записей: ${logs?.length ?? 0}`;
    const tb = document.getElementById('tbody');
    if (!logs?.length) {
      tb.innerHTML = '<tr><td colspan="5" class="empty">Логов не найдено</td></tr>';
      return;
    }
    tb.innerHTML = logs
      .map((e) => {
        const fields =
          e.fields && Object.keys(e.fields).length
            ? `<div class="fields">${esc(JSON.stringify(e.fields))}</div>`
            : '';
        return `<tr>
        <td class="ts">${fmtTs(e.timestamp)}</td>
        <td class="svc">${esc(e.service)}</td>
        <td>${badge(e.level)}</td>
        <td class="msg">${esc(e.message)}${fields}</td>
        <td class="tid">${esc(e.trace_id || '')}</td>
      </tr>`;
      })
      .join('');
  } catch (e) {
    if (e.name !== 'AbortError') {
      console.error('logs error:', e);
    }
  }
}

function applyFilters() {
  fetchLogs();
}

function resetFilters() {
  ['f-svc', 'f-q'].forEach((id) => (document.getElementById(id).value = ''));
  document.getElementById('f-lvl').value = '';
  document.getElementById('f-n').value = '100';
  fetchLogs();
}

function startTimer() {
  clearInterval(timer);
  cd = TICK;
  timer = setInterval(() => {
    document.getElementById('cd').textContent = --cd;
    if (cd <= 0) {
      fetchStats();
      fetchLogs();
      cd = TICK;
    }
  }, 1000);
}

// Первоначальная загрузка и старт таймера
fetchStats();
fetchLogs();
startTimer();
