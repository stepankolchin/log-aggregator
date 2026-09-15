const TICK = 5;
let cd = TICK;
let timer = null;
let logsAbortController = null;
let currentOffset = 0;
let hasMoreLogs = false;

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

// Безопасное экранирование HTML-сущностей
function esc(s) {
  if (s === null || s === undefined) return '';
  return String(s)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

// Форматирование временных меток в читаемый вид
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

// Генерация бейджа уровня логирования
function badge(lvl) {
  const safe = esc(lvl || 'info');
  return `<span class="badge lv-${safe}">${safe}</span>`;
}

// Безопасная установка текста элемента
function setTxt(id, val) {
  const el = document.getElementById(id);
  if (el) {
    el.textContent = val !== null && val !== undefined ? String(val) : '—';
  }
}

// Безопасная установка HTML содержимого
function setHtml(id, val) {
  const el = document.getElementById(id);
  if (el) {
    el.innerHTML = val !== null && val !== undefined ? String(val) : '';
  }
}

// Отрисовка гистограмм статистики
function renderBars(id, data, colorFn) {
  const el = document.getElementById(id);
  if (!el) return;

  if (!data || typeof data !== 'object' || Object.keys(data).length === 0) {
    el.innerHTML = '<div style="color:var(--muted);font-size:13px">Нет данных</div>';
    return;
  }

  const entries = Object.entries(data).filter(([, v]) => typeof v === 'number');
  if (entries.length === 0) {
    el.innerHTML = '<div style="color:var(--muted);font-size:13px">Нет данных</div>';
    return;
  }

  const max = Math.max(...entries.map(([, v]) => v), 0);
  entries.sort((a, b) => b[1] - a[1]);

  el.innerHTML = entries
    .map(([k, val], i) => {
      const pct = max > 0 ? ((val / max) * 100).toFixed(1) : '0';
      const color = colorFn ? colorFn(k, i) : '#3b82f6';
      return `<div class="bar-row">
      <div class="bar-key" title="${esc(k)}">${esc(k)}</div>
      <div class="bar-track"><div class="bar-fill" style="width:${pct}%;background:${esc(color)}"></div></div>
      <div class="bar-cnt">${val}</div>
    </div>`;
    })
    .join('');
}

// Загрузка статистики агрегатора
async function fetchStats() {
  try {
    const res = await fetch('/api/v1/stats');
    if (!res.ok) {
      throw new Error(`HTTP ${res.status}`);
    }
    const d = await res.json();
    setTxt('s-acc', d.accepted ?? '—');
    setTxt('s-drp', d.dropped ?? '—');
    setTxt('s-err', d.errors ?? '—');
    setTxt('s-errlvl', d.by_level?.error ?? 0);
    setTxt('s-mem', d.in_memory ?? '—');
    setTxt('s-up', d.uptime ?? '—');

    renderBars('by-svc', d.by_service, (k, i) => svcColors[i % svcColors.length]);
    renderBars('by-lvl', d.by_level, (k) => lvlColor[k] || '#3b82f6');
  } catch (e) {
    console.error('stats error:', e);
  }
}

// Переключение источника: Live (RAM) vs Архив (диск)
function onSourceChange() {
  const srcEl = document.getElementById('f-src');
  const src = srcEl ? srcEl.value : 'memory';
  const isArchive = src === 'file';

  const wrapFrom = document.getElementById('wrap-from');
  const wrapTo = document.getElementById('wrap-to');
  if (wrapFrom) wrapFrom.style.display = isArchive ? 'flex' : 'none';
  if (wrapTo) wrapTo.style.display = isArchive ? 'flex' : 'none';

  setTxt('logs-title', isArchive ? 'Архивные логи (Диск)' : 'Последние логи (Live RAM)');
  currentOffset = 0;
  fetchLogs();
}

// Запрос и отображение логов с учётом фильтров и пагинации
async function fetchLogs() {
  if (logsAbortController) {
    logsAbortController.abort();
  }
  logsAbortController = new AbortController();

  const src = document.getElementById('f-src')?.value || 'memory';
  const limit = document.getElementById('f-n')?.value || '100';

  const p = new URLSearchParams({
    source: src,
    service: document.getElementById('f-svc')?.value?.trim() || '',
    level: document.getElementById('f-lvl')?.value || '',
    search: document.getElementById('f-q')?.value?.trim() || '',
    limit: limit,
    offset: String(currentOffset),
  });

  if (src === 'file') {
    const fromVal = document.getElementById('f-from')?.value;
    const toVal = document.getElementById('f-to')?.value;
    if (fromVal) p.set('from', fromVal);
    if (toVal) p.set('to', toVal);
  }

  [...p.keys()].forEach((k) => {
    if (!p.get(k)) p.delete(k);
  });

  try {
    const res = await fetch('/api/v1/logs?' + p.toString(), { signal: logsAbortController.signal });
    if (!res.ok) {
      const errData = await res.json().catch(() => ({}));
      const msg = errData.error || `Ошибка сервера (${res.status})`;
      setHtml('tbody', `<tr><td colspan="5" class="empty" style="color:var(--error)">${esc(msg)}</td></tr>`);
      setTxt('log-cnt', 'ошибка');
      return;
    }

    hasMoreLogs = res.headers.get('X-Has-More') === 'true';
    const logs = await res.json();

    const limitNum = Number(limit) || 100;
    const pageNum = Math.floor(currentOffset / limitNum) + 1;
    const pageStr = `Стр. ${pageNum}`;

    ['page-num', 'page-num-b'].forEach((id) => setTxt(id, pageStr));

    const hasPrev = currentOffset > 0;
    const hasNext = hasMoreLogs;

    ['btn-prev', 'btn-prev-b'].forEach((id) => {
      const el = document.getElementById(id);
      if (el) el.disabled = !hasPrev;
    });
    ['btn-next', 'btn-next-b'].forEach((id) => {
      const el = document.getElementById(id);
      if (el) el.disabled = !hasNext;
    });

    const cntText = `записей: ${Array.isArray(logs) ? logs.length : 0}${hasMoreLogs ? '+' : ''} (смещение: ${currentOffset})`;
    setTxt('log-cnt', cntText);

    const tb = document.getElementById('tbody');
    if (!tb) return;

    if (!Array.isArray(logs) || logs.length === 0) {
      tb.innerHTML = '<tr><td colspan="5" class="empty">Логов не найдено</td></tr>';
      return;
    }

    tb.innerHTML = logs
      .map((e) => {
        const hasFields = e && e.fields && typeof e.fields === 'object' && Object.keys(e.fields).length > 0;
        const fieldsHtml = hasFields
          ? `<div class="fields">${esc(JSON.stringify(e.fields))}</div>`
          : '';
        const displayTs = e.timestamp || e.server_timestamp;
        const tsTitle = e.timestamp && e.server_timestamp
          ? `Client: ${esc(e.timestamp)}\nServer: ${esc(e.server_timestamp)}`
          : esc(displayTs);

        return `<tr>
        <td class="ts" title="${tsTitle}">${fmtTs(displayTs)}</td>
        <td class="svc">${esc(e.service || '—')}</td>
        <td>${badge(e.level)}</td>
        <td class="msg">${esc(e.message || '')}${fieldsHtml}</td>
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

// Предыдущая страница
function prevPage() {
  const limit = Number(document.getElementById('f-n')?.value) || 100;
  currentOffset = Math.max(0, currentOffset - limit);
  fetchLogs();
}

// Следующая страница
function nextPage() {
  if (!hasMoreLogs) return;
  const limit = Number(document.getElementById('f-n')?.value) || 100;
  currentOffset += limit;
  fetchLogs();
}

// Применить фильтры
function applyFilters() {
  currentOffset = 0;
  fetchLogs();
}

// Сбросить фильтры к значениям по умолчанию
function resetFilters() {
  ['f-svc', 'f-q', 'f-from', 'f-to'].forEach((id) => {
    const el = document.getElementById(id);
    if (el) el.value = '';
  });
  const lvlEl = document.getElementById('f-lvl');
  if (lvlEl) lvlEl.value = '';

  const nEl = document.getElementById('f-n');
  if (nEl) nEl.value = '100';

  const srcEl = document.getElementById('f-src');
  if (srcEl) srcEl.value = 'memory';

  onSourceChange();
}

// Автоматическое периодическое обновление данных
function startTimer() {
  if (timer) clearInterval(timer);
  cd = TICK;
  timer = setInterval(() => {
    cd--;
    setTxt('cd', cd);
    if (cd <= 0) {
      fetchStats();
      const isLive = (document.getElementById('f-src')?.value || 'memory') === 'memory';
      if (isLive && currentOffset === 0) {
        fetchLogs();
      }
      cd = TICK;
    }
  }, 1000);
}

// Обработка нажатия Enter в полях фильтров
function setupKeyListeners() {
  ['f-svc', 'f-q', 'f-from', 'f-to'].forEach((id) => {
    const el = document.getElementById(id);
    if (el) {
      el.addEventListener('keydown', (e) => {
        if (e.key === 'Enter') {
          applyFilters();
        }
      });
    }
  });
}

// Инициализация при загрузке
setupKeyListeners();
fetchStats();
fetchLogs();
startTimer();
