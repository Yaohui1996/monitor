'use strict';

(() => {
  // ---------------------------------------------------------------- helpers

  const $ = (id) => document.getElementById(id);
  const num = (v) => (Number.isFinite(Number(v)) ? Number(v) : 0);
  const clamp = (v, lo, hi) => Math.min(Math.max(v, lo), hi);
  const MONO = '10px ui-monospace, SFMono-Regular, Menlo, monospace';
  const SANS = '11px -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", sans-serif';

  const params = new URLSearchParams(location.search);
  const token = params.get('token') || '';

  function apiPath(path) {
    if (!token) return path;
    return path + (path.includes('?') ? '&' : '?') + 'token=' + encodeURIComponent(token);
  }

  function store(key, value) {
    try { localStorage.setItem(key, value); } catch (_) { /* private mode */ }
  }

  function restore(key) {
    try { return localStorage.getItem(key); } catch (_) { return null; }
  }

  function fmtBytes(value, digits) {
    const n = num(value);
    if (n <= 0) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
    let i = 0;
    let v = n;
    while (v >= 1024 && i < units.length - 1) {
      v /= 1024;
      i += 1;
    }
    if (digits != null) return `${v.toFixed(digits)} ${units[i]}`;
    const s = v >= 100 ? v.toFixed(0) : v >= 10 ? v.toFixed(1) : v.toFixed(2);
    return `${s} ${units[i]}`;
  }

  function fmtRate(value) {
    return `${fmtBytes(value)}/s`;
  }

  function fmtCompactBytes(value) {
    const n = num(value);
    if (n <= 0) return '0';
    const units = ['B', 'K', 'M', 'G', 'T'];
    let i = 0;
    let v = n;
    while (v >= 1024 && i < units.length - 1) {
      v /= 1024;
      i += 1;
    }
    const s = v >= 100 ? v.toFixed(0) : v >= 10 ? v.toFixed(1) : v.toFixed(2);
    return s + units[i];
  }

  function fmtPercent(value, digits = 1) {
    return `${num(value).toFixed(digits)}%`;
  }

  function fmtOps(value) {
    const n = num(value);
    return n >= 100 ? String(Math.round(n)) : n.toFixed(1);
  }

  function fmtUptime(seconds) {
    const s = num(seconds);
    if (s <= 0) return '—';
    const d = Math.floor(s / 86400);
    const h = Math.floor((s % 86400) / 3600);
    const m = Math.floor((s % 3600) / 60);
    if (d > 0) return `${d}d ${h}h`;
    if (h > 0) return `${h}h ${m}m`;
    if (m > 0) return `${m}m`;
    return `${Math.floor(s)}s`;
  }

  function fmtSeconds(seconds) {
    const s = num(seconds);
    if (s > 0 && s < 1) return `${Math.round(s * 1000)}ms`;
    return `${s}s`;
  }

  function pad2(n) {
    return String(n).padStart(2, '0');
  }

  function fmtClock(ms, withSeconds = true) {
    const d = new Date(ms);
    const hm = `${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
    return withSeconds ? `${hm}:${pad2(d.getSeconds())}` : hm;
  }

  function fmtDateTime(ms) {
    const d = new Date(ms);
    return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())} ${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
  }

  function setText(id, text) {
    const node = $(id);
    if (node) node.textContent = text;
  }

  function el(tag, props, ...children) {
    const node = document.createElement(tag);
    for (const [key, value] of Object.entries(props || {})) {
      if (key === 'class') node.className = value;
      else if (key === 'text') node.textContent = value;
      else node.setAttribute(key, value);
    }
    for (const child of children) {
      if (child != null) node.append(child);
    }
    return node;
  }

  function hexAlpha(hex, alpha) {
    const h = hex.replace('#', '');
    const full = h.length === 3 ? h.split('').map((c) => c + c).join('') : h;
    const r = parseInt(full.slice(0, 2), 16);
    const g = parseInt(full.slice(2, 4), 16);
    const b = parseInt(full.slice(4, 6), 16);
    return `rgba(${r}, ${g}, ${b}, ${alpha})`;
  }

  function niceCeil(value) {
    const v = num(value);
    if (v <= 0) return 1;
    const exp = Math.floor(Math.log10(v));
    const base = Math.pow(10, exp);
    const n = v / base;
    const mult = n <= 1 ? 1 : n <= 2 ? 2 : n <= 5 ? 5 : 10;
    return mult * base;
  }

  // ---------------------------------------------------------------- state

  const state = {
    points: [],
    snapshot: null,
    meta: null,
    windowSec: 300,
    procSort: 'cpu',
    stream: null,
    pollTimer: null,
    booted: false,
  };

  const savedWindow = Number(restore('monitor.window'));
  if ([60, 300, 900, 3600].includes(savedWindow)) state.windowSec = savedWindow;
  const savedSort = restore('monitor.procSort');
  if (savedSort === 'cpu' || savedSort === 'mem') state.procSort = savedSort;

  // ---------------------------------------------------------------- chart

  class TimeChart {
    constructor(canvas, config) {
      this.canvas = canvas;
      this.ctx = canvas.getContext('2d');
      this.config = config;
      this.points = [];
      this.windowSec = state.windowSec;
      this.hover = null;
      this.geom = null;
      this.w = 0;
      this.h = 0;
      this.dpr = 1;
      this.raf = 0;
      this.tooltip = $('chartTooltip');

      new ResizeObserver(() => {
        this.resize();
        this.requestDraw();
      }).observe(canvas);

      canvas.addEventListener('pointermove', (e) => this.onMove(e));
      canvas.addEventListener('pointerleave', () => this.onLeave());
      this.resize();
    }

    resize() {
      const rect = this.canvas.getBoundingClientRect();
      this.dpr = Math.min(window.devicePixelRatio || 1, 2);
      this.w = rect.width;
      this.h = rect.height;
      this.canvas.width = Math.max(1, Math.round(this.w * this.dpr));
      this.canvas.height = Math.max(1, Math.round(this.h * this.dpr));
      this.ctx.setTransform(this.dpr, 0, 0, this.dpr, 0, 0);
    }

    requestDraw() {
      if (this.raf) return;
      this.raf = requestAnimationFrame(() => {
        this.raf = 0;
        this.draw();
      });
    }

    setPoints(points) {
      this.points = points;
      this.requestDraw();
    }

    setWindow(seconds) {
      this.windowSec = seconds;
      this.hover = null;
      this.hideTooltip();
      this.requestDraw();
    }

    onLeave() {
      this.hover = null;
      this.hideTooltip();
      this.requestDraw();
    }

    onMove(event) {
      const g = this.geom;
      if (!g) return;
      const rect = this.canvas.getBoundingClientRect();
      const x = event.clientX - rect.left;
      if (x < g.x || x > g.x + g.w) {
        this.onLeave();
        return;
      }

      const now = Date.now();
      const windowMs = this.windowSec * 1000;
      const cutoff = now - windowMs;
      const target = cutoff + ((x - g.x) / g.w) * windowMs;

      let best = null;
      let bestDelta = Infinity;
      for (const p of this.points) {
        if (p.t < cutoff - 1000) continue;
        const delta = Math.abs(p.t - target);
        if (delta < bestDelta) {
          bestDelta = delta;
          best = p;
        }
      }
      if (!best) return;

      this.hover = best;
      this.showTooltip(best, event.clientX, event.clientY);
      this.requestDraw();
    }

    showTooltip(point, clientX, clientY) {
      if (!this.tooltip) return;
      const rows = this.config.series.map((series) => {
        const row = el('span', { class: 'tt-row ' + series.key });
        row.append(
          el('i'),
          el('span', { text: series.label }),
          el('b', { text: series.format(point[series.key]) })
        );
        return row;
      });
      this.tooltip.replaceChildren(
        el('span', { class: 'tt-time', text: fmtClock(point.t) }),
        ...rows
      );
      this.tooltip.hidden = false;
      const x = clamp(clientX, 92, window.innerWidth - 92);
      this.tooltip.style.left = `${Math.round(x)}px`;
      this.tooltip.style.top = `${Math.round(clientY)}px`;
    }

    hideTooltip() {
      if (this.tooltip) this.tooltip.hidden = true;
    }

    draw() {
      const ctx = this.ctx;
      if (!ctx || this.w <= 0 || this.h <= 0) return;
      ctx.clearRect(0, 0, this.w, this.h);

      const pad = { l: 48, r: 12, t: 12, b: 22 };
      const g = {
        x: pad.l,
        y: pad.t,
        w: Math.max(0, this.w - pad.l - pad.r),
        h: Math.max(0, this.h - pad.t - pad.b),
      };
      this.geom = g;
      if (g.w < 24 || g.h < 24) return;

      const now = Date.now();
      const windowMs = this.windowSec * 1000;
      const cutoff = now - windowMs;
      const series = this.config.series;
      const pts = this.points.filter((p) => p && p.t >= cutoff - 1000);

      let maxValue = 0;
      for (const p of pts) {
        for (const s of series) maxValue = Math.max(maxValue, num(p[s.key]));
      }
      const yMax = this.config.yMax || niceCeil(maxValue * 1.15) || 1;
      const xFor = (t) => g.x + ((t - cutoff) / windowMs) * g.w;
      const yFor = (v) => g.y + g.h - (clamp(v, 0, yMax) / yMax) * g.h;

      ctx.save();
      ctx.font = MONO;
      ctx.lineWidth = 1;
      ctx.strokeStyle = 'rgba(148, 163, 184, 0.09)';
      ctx.fillStyle = 'rgba(100, 116, 139, 0.95)';
      ctx.textAlign = 'right';
      ctx.textBaseline = 'middle';
      for (let i = 0; i <= 4; i += 1) {
        const y = Math.round(g.y + (g.h * i) / 4) + 0.5;
        ctx.beginPath();
        ctx.moveTo(g.x, y);
        ctx.lineTo(g.x + g.w, y);
        ctx.stroke();
        const value = yMax - (yMax * i) / 4;
        ctx.fillText(this.config.formatY(value), g.x - 8, y);
      }

      ctx.textAlign = 'center';
      ctx.textBaseline = 'top';
      for (let i = 0; i <= 4; i += 1) {
        const frac = i / 4;
        const t = cutoff + frac * windowMs;
        const x = clamp(g.x + frac * g.w, g.x + 24, g.x + g.w - 24);
        ctx.fillText(fmtClock(t, windowMs <= 600000), x, g.y + g.h + 8);
      }
      ctx.restore();

      if (pts.length < 2) {
        ctx.save();
        ctx.font = SANS;
        ctx.fillStyle = 'rgba(100, 116, 139, 0.9)';
        ctx.textAlign = 'center';
        ctx.textBaseline = 'middle';
        ctx.fillText('正在采集数据…', g.x + g.w / 2, g.y + g.h / 2);
        ctx.restore();
        return;
      }

      ctx.save();
      ctx.beginPath();
      ctx.rect(g.x, g.y, g.w, g.h);
      ctx.clip();

      for (const s of series) {
        const line = new Path2D();
        let started = false;
        let firstX = 0;
        let lastX = 0;
        for (const p of pts) {
          const x = xFor(p.t);
          const y = yFor(num(p[s.key]));
          if (!started) {
            line.moveTo(x, y);
            firstX = x;
            started = true;
          } else {
            line.lineTo(x, y);
          }
          lastX = x;
        }
        if (!started) continue;

        if (s.fill) {
          const area = new Path2D(line);
          area.lineTo(lastX, g.y + g.h);
          area.lineTo(firstX, g.y + g.h);
          area.closePath();
          const grad = ctx.createLinearGradient(0, g.y, 0, g.y + g.h);
          grad.addColorStop(0, hexAlpha(s.color, 0.28));
          grad.addColorStop(1, hexAlpha(s.color, 0.01));
          ctx.fillStyle = grad;
          ctx.fill(area);
        }

        ctx.strokeStyle = s.color;
        ctx.lineWidth = 1.8;
        ctx.lineJoin = 'round';
        ctx.lineCap = 'round';
        ctx.stroke(line);
      }
      ctx.restore();

      const latest = pts[pts.length - 1];
      for (const s of series) {
        const x = clamp(xFor(latest.t), g.x, g.x + g.w);
        const y = yFor(num(latest[s.key]));
        ctx.beginPath();
        ctx.arc(x, y, 5, 0, Math.PI * 2);
        ctx.fillStyle = hexAlpha(s.color, 0.16);
        ctx.fill();
        ctx.beginPath();
        ctx.arc(x, y, 2.4, 0, Math.PI * 2);
        ctx.fillStyle = s.color;
        ctx.fill();
      }

      if (this.hover && this.hover.t >= cutoff - 1000) {
        const x = clamp(xFor(this.hover.t), g.x, g.x + g.w);
        ctx.save();
        ctx.setLineDash([3, 3]);
        ctx.strokeStyle = 'rgba(148, 163, 184, 0.4)';
        ctx.lineWidth = 1;
        ctx.beginPath();
        ctx.moveTo(x, g.y);
        ctx.lineTo(x, g.y + g.h);
        ctx.stroke();
        ctx.restore();

        for (const s of series) {
          const y = yFor(num(this.hover[s.key]));
          ctx.beginPath();
          ctx.arc(x, y, 3, 0, Math.PI * 2);
          ctx.fillStyle = '#060a0f';
          ctx.fill();
          ctx.strokeStyle = s.color;
          ctx.lineWidth = 1.6;
          ctx.stroke();
        }
      }
    }
  }

  const usageChart = new TimeChart($('usageChart'), {
    series: [
      { key: 'cpu', label: 'CPU', color: '#22d3ee', fill: true, format: (v) => fmtPercent(v) },
      { key: 'mem', label: '内存', color: '#a78bfa', format: (v) => fmtPercent(v) },
    ],
    yMax: 100,
    formatY: (v) => `${Math.round(v)}%`,
  });

  const networkChart = new TimeChart($('networkChart'), {
    series: [
      { key: 'rx', label: '下行', color: '#34d399', fill: true, format: fmtRate },
      { key: 'tx', label: '上行', color: '#fbbf24', format: fmtRate },
    ],
    formatY: fmtCompactBytes,
  });

  // ---------------------------------------------------------------- rendering

  function renderHost(host) {
    if (!host) return;
    const name = host.hostname || '本机监控';
    setText('hostname', name);
    document.title = `${name} · Monitor`;
    const platform = [host.platform, host.platformVersion].filter(Boolean).join(' ');
    const bits = [platform, host.kernelArch, host.cpuModel].filter(Boolean);
    setText('hostMeta', bits.join(' · ') || '—');
    setText('uptimeValue', fmtUptime(host.uptime));
  }

  function renderCPU(cpu) {
    if (!cpu) return;
    const pct = clamp(num(cpu.percent), 0, 100);
    setText('cpuValue', pct.toFixed(0));
    const cores = num(cpu.cores) || (cpu.perCore || []).length;
    setText('cpuSub', cores ? `${cores} 逻辑核心` : '—');
    setText('cpuUser', fmtPercent(cpu.user));
    setText('cpuSystem', fmtPercent(cpu.system));
    setText('cpuIowait', fmtPercent(cpu.iowait));

    const gauge = $('cpuGauge');
    gauge.style.setProperty('--pct', `${pct.toFixed(1)}%`);
    gauge.classList.toggle('warn', pct >= 70 && pct < 90);
    gauge.classList.toggle('crit', pct >= 90);
    gauge.setAttribute('aria-label', `CPU 使用率 ${pct.toFixed(1)}%`);

    renderCores(cpu.perCore || []);
  }

  function renderCores(perCore) {
    const container = $('cores');
    if (container.childElementCount !== perCore.length) {
      container.replaceChildren(
        ...perCore.map((_, index) => {
          const bar = el('div', { class: 'core', title: `逻辑核心 ${index}` });
          bar.append(el('span'));
          return bar;
        })
      );
    }
    for (let i = 0; i < perCore.length; i += 1) {
      const bar = container.children[i];
      if (!bar) continue;
      const value = clamp(num(perCore[i]), 0, 100);
      bar.firstElementChild.style.height = `${Math.max(2, value)}%`;
      bar.title = `逻辑核心 ${i} · ${value.toFixed(0)}%`;
      bar.classList.toggle('hot', value >= 85);
    }
  }

  function renderMemory(memory) {
    if (!memory) return;
    const pct = clamp(num(memory.percent), 0, 100);
    setText('memValue', pct.toFixed(1));
    setText('memSub', `${fmtBytes(memory.used)} / ${fmtBytes(memory.total)}`);
    $('memBar').style.width = `${pct}%`;
    const wrap = $('memBarWrap');
    wrap.classList.toggle('warn', pct >= 75 && pct < 90);
    wrap.classList.toggle('crit', pct >= 90);

    setText('memAvail', fmtBytes(memory.available));
    const cache = num(memory.cached) + num(memory.buffers);
    setText('memCached', cache > 0 ? fmtBytes(cache) : '—');
    setText('memUsed', fmtBytes(memory.used));
    setText(
      'memSwap',
      num(memory.swapTotal) > 0
        ? `${fmtBytes(memory.swapUsed)} / ${fmtBytes(memory.swapTotal)}`
        : '未启用'
    );
  }

  function renderNetwork(network) {
    if (!network) return;
    setText('netRx', fmtRate(network.rxBytesPerSec));
    setText('netTx', fmtRate(network.txBytesPerSec));
    const interfaces = Array.isArray(network.interfaces) ? network.interfaces : [];
    setText('netSub', interfaces.length ? `${interfaces.length} 个网卡` : '无活动网卡');
    setText('netRxTotal', fmtBytes(network.rxBytesTotal));
    setText('netTxTotal', fmtBytes(network.txBytesTotal));
  }

  function renderDiskIO(io, disks) {
    if (!io) return;
    setText('ioRead', fmtRate(io.readBytesPerSec));
    setText('ioWrite', fmtRate(io.writeBytesPerSec));
    setText('ioReadOps', fmtOps(io.readOpsPerSec));
    setText('ioWriteOps', fmtOps(io.writeOpsPerSec));
    setText('ioSub', `${(disks || []).length} 个分区`);
  }

  function renderDisks(disks) {
    const body = $('diskBody');
    if (!Array.isArray(disks) || disks.length === 0) {
      body.replaceChildren(el('tr', {}, el('td', { colspan: '4', class: 'empty', text: '暂无磁盘数据' })));
      setText('diskCount', '—');
      return;
    }
    setText('diskCount', `${disks.length} 个挂载点`);

    body.replaceChildren(
      ...disks.map((disk) => {
        const mount = el(
          'div',
          { class: 'mount' },
          el('b', { text: disk.mountpoint || '—' }),
          el('span', { text: disk.device || '' })
        );
        const usage = el(
          'div',
          { class: 'usage-cell' },
          el('b', { text: fmtBytes(disk.used) }),
          el('span', { text: `/ ${fmtBytes(disk.total)}` })
        );
        const pct = clamp(num(disk.percent), 0, 100);
        const meter = el('div', {
          class: 'meter' + (pct >= 90 ? ' crit' : pct >= 75 ? ' warn' : ''),
        });
        const fill = el('span');
        fill.style.width = `${pct}%`;
        meter.append(fill);
        const meterCell = el(
          'div',
          { class: 'meter-line' },
          meter,
          el('span', { class: 'pct', text: `${Math.round(pct)}%` })
        );
        return el(
          'tr',
          {},
          el('td', {}, mount),
          el('td', {}, el('span', { class: 'fs', text: disk.fstype || '—' })),
          el('td', { class: 'num' }, usage),
          el('td', { class: 'meter-col' }, meterCell)
        );
      })
    );
  }

  function renderProcesses(procs) {
    const body = $('procBody');
    if (!Array.isArray(procs) || procs.length === 0) {
      body.replaceChildren(el('tr', {}, el('td', { colspan: '5', class: 'empty', text: '暂无进程数据' })));
      return;
    }

    const sorted = [...procs]
      .sort((a, b) =>
        state.procSort === 'mem'
          ? num(b.memPercent) - num(a.memPercent)
          : num(b.cpuPercent) - num(a.cpuPercent)
      )
      .slice(0, 12);

    body.replaceChildren(
      ...sorted.map((proc) => {
        const name = String(proc.name || `pid ${proc.pid}`);
        const cpu = num(proc.cpuPercent);
        const initial = [...name.trim()][0] || '?';
        const avatar = el('span', {
          class: 'avatar c' + (Math.abs(num(proc.pid)) % 4),
          text: initial.toUpperCase(),
        });
        return el(
          'tr',
          {},
          el('td', {}, el('div', { class: 'proc' }, avatar, el('span', { class: 'pname', text: name }))),
          el('td', { class: 'num mono muted', text: String(proc.pid ?? '—') }),
          el('td', {
            class: 'num mono' + (cpu >= 90 ? ' crit' : cpu >= 40 ? ' hot' : ''),
            text: fmtPercent(cpu),
          }),
          el('td', { class: 'num mono', text: fmtPercent(proc.memPercent) }),
          el('td', { class: 'num mono muted', text: fmtBytes(proc.rss) })
        );
      })
    );
  }

  function renderLoad(load) {
    const chip = $('loadChip');
    if (!load) {
      chip.hidden = true;
      return;
    }
    chip.hidden = false;
    setText(
      'loadValue',
      `${num(load.load1).toFixed(2)} · ${num(load.load5).toFixed(2)} · ${num(load.load15).toFixed(2)}`
    );
  }

  function renderInfo() {
    const snap = state.snapshot;
    if (!snap) return;
    const host = snap.host || {};
    const memory = snap.memory || {};
    const interval = state.meta ? state.meta.interval : null;

    const signature = [
      host.platform,
      host.platformVersion,
      host.kernelVersion,
      host.kernelArch,
      host.cpuModel,
      host.cpuCores,
      host.cpuPhysical,
      host.bootTime,
      memory.total,
      snap.processCount,
      interval,
    ].join('|');
    if ($('infoGrid').dataset.signature === signature) return;
    $('infoGrid').dataset.signature = signature;

    const items = [
      ['操作系统', [host.platform, host.platformVersion].filter(Boolean).join(' ') || '—'],
      ['内核', [host.kernelVersion, host.kernelArch].filter(Boolean).join(' · ') || '—'],
      ['处理器', host.cpuModel || '—'],
      [
        '核心',
        host.cpuCores
          ? `${host.cpuCores} 逻辑${host.cpuPhysical ? ` / ${host.cpuPhysical} 物理` : ''}`
          : '—',
      ],
      ['总内存', fmtBytes(memory.total)],
      ['开机时间', host.bootTime ? fmtDateTime(host.bootTime * 1000) : '—'],
      ['采样间隔', interval != null ? fmtSeconds(interval) : '—'],
      ['进程总数', snap.processCount != null ? String(snap.processCount) : '—'],
    ];

    $('infoGrid').replaceChildren(
      ...items.map(([label, value]) =>
        el('div', { class: 'info-item' }, el('span', { text: label }), el('b', { text: value }))
      )
    );
  }

  function renderFooter() {
    if (!state.meta) return;
    setText('footVersion', state.meta.version || 'dev');
    setText('footInterval', fmtSeconds(state.meta.interval));
  }

  function updateLegends() {
    const snap = state.snapshot;
    if (!snap) return;
    setText('legendCpu', fmtPercent(snap.cpu && snap.cpu.percent));
    setText('legendMem', fmtPercent(snap.memory && snap.memory.percent));
    setText('legendRx', fmtRate(snap.network && snap.network.rxBytesPerSec));
    setText('legendTx', fmtRate(snap.network && snap.network.txBytesPerSec));
  }

  // ---------------------------------------------------------------- data flow

  let chartRaf = 0;
  function scheduleCharts() {
    if (chartRaf) return;
    chartRaf = requestAnimationFrame(() => {
      chartRaf = 0;
      usageChart.setPoints(state.points);
      networkChart.setPoints(state.points);
    });
  }

  function pointFrom(snap) {
    const cpu = snap.cpu || {};
    const memory = snap.memory || {};
    const network = snap.network || {};
    const io = snap.diskIO || {};
    return {
      t: num(snap.timestamp),
      cpu: num(cpu.percent),
      mem: num(memory.percent),
      swap: num(memory.swapPercent),
      load: num(snap.load && snap.load.load1),
      rx: num(network.rxBytesPerSec),
      tx: num(network.txBytesPerSec),
      read: num(io.readBytesPerSec),
      write: num(io.writeBytesPerSec),
    };
  }

  function pushPoint(point) {
    if (!point.t) return;
    const points = state.points;
    const last = points[points.length - 1];
    if (last && point.t <= last.t) {
      if (point.t === last.t) points[points.length - 1] = point;
      return;
    }
    points.push(point);
    if (points.length > 7200) points.splice(0, points.length - 7200);
    scheduleCharts();
  }

  function applySnapshot(snap) {
    if (!snap || !snap.timestamp) return;
    state.snapshot = snap;
    renderHost(snap.host);
    renderCPU(snap.cpu);
    renderMemory(snap.memory);
    renderNetwork(snap.network);
    renderDiskIO(snap.diskIO, snap.disks);
    renderDisks(snap.disks);
    renderProcesses(snap.processes);
    renderLoad(snap.load);
    renderInfo();
    updateLegends();
    pushPoint(pointFrom(snap));
  }

  function applyHistory(history) {
    if (!history || !Array.isArray(history.t)) return;
    const points = new Array(history.t.length);
    for (let i = 0; i < history.t.length; i += 1) {
      points[i] = {
        t: num(history.t[i]),
        cpu: num(history.cpu[i]),
        mem: num(history.mem[i]),
        swap: num(history.swap[i]),
        load: num(history.load[i]),
        rx: num(history.rx[i]),
        tx: num(history.tx[i]),
        read: num(history.read[i]),
        write: num(history.write[i]),
      };
    }
    state.points = points;
    usageChart.setPoints(points);
    networkChart.setPoints(points);
  }

  async function fetchJSON(path) {
    const res = await fetch(apiPath(path), {
      cache: 'no-store',
      headers: { Accept: 'application/json' },
    });
    if (!res.ok) {
      const err = new Error(`HTTP ${res.status}`);
      err.status = res.status;
      throw err;
    }
    return res.json();
  }

  // ---------------------------------------------------------------- connection

  function setConnected(live, text) {
    const chip = $('connChip');
    const down = text === '连接断开' || text === '未授权';
    chip.classList.toggle('live', live);
    chip.classList.toggle('down', down);
    chip.classList.toggle('connecting', !live && !down);
    setText('connText', text);
  }

  function connectStream() {
    if (state.stream || state.pollTimer) return;
    if (!('EventSource' in window)) {
      startPolling();
      return;
    }
    setConnected(false, '连接中');
    const stream = new EventSource(apiPath('/api/stream'));
    state.stream = stream;
    stream.addEventListener('open', () => setConnected(true, '实时'));
    stream.addEventListener('tick', (event) => {
      let snap;
      try {
        snap = JSON.parse(event.data);
      } catch (_) {
        return;
      }
      setConnected(true, '实时');
      applySnapshot(snap);
    });
    stream.onerror = () => {
      setConnected(false, '重连中');
      if (stream.readyState === EventSource.CLOSED) {
        stream.close();
        state.stream = null;
        startPolling();
      }
    };
  }

  function startPolling() {
    if (state.pollTimer) return;
    const tick = async () => {
      try {
        const snap = await fetchJSON('/api/snapshot');
        setConnected(true, '轮询模式');
        applySnapshot(snap);
      } catch (_) {
        setConnected(false, '连接断开');
      }
    };
    tick();
    state.pollTimer = setInterval(tick, 2000);
  }

  let toastTimer = 0;
  function showToast(message) {
    const toast = $('toast');
    toast.textContent = message;
    toast.hidden = false;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => { toast.hidden = true; }, 7000);
  }

  async function bootstrap() {
    try {
      const [meta, history, snap] = await Promise.all([
        fetchJSON('/api/meta'),
        fetchJSON('/api/history?seconds=3600'),
        fetchJSON('/api/snapshot'),
      ]);
      state.meta = meta;
      state.points = [];
      applyHistory(history);
      applySnapshot(snap);
      renderFooter();
      $('toast').hidden = true;
      state.booted = true;
      connectStream();
    } catch (err) {
      if (err && err.status === 401) {
        showToast('需要访问令牌，请在地址后追加 ?token=…');
        setConnected(false, '未授权');
        return;
      }
      showToast('无法连接采集服务，正在重试…');
      setConnected(false, '连接断开');
      setTimeout(bootstrap, 2500);
    }
  }

  // ---------------------------------------------------------------- controls

  function setupRangePicker() {
    const picker = $('rangePicker');
    const buttons = [...picker.querySelectorAll('button')];
    for (const button of buttons) {
      const seconds = Number(button.dataset.seconds);
      button.classList.toggle('active', seconds === state.windowSec);
      button.addEventListener('click', () => {
        state.windowSec = seconds;
        store('monitor.window', String(seconds));
        for (const b of buttons) b.classList.toggle('active', b === button);
        usageChart.setWindow(seconds);
        networkChart.setWindow(seconds);
      });
    }
  }

  function setupProcSort() {
    const group = $('procSort');
    const buttons = [...group.querySelectorAll('button')];
    for (const button of buttons) {
      button.classList.toggle('active', button.dataset.sort === state.procSort);
      button.addEventListener('click', () => {
        state.procSort = button.dataset.sort === 'mem' ? 'mem' : 'cpu';
        store('monitor.procSort', state.procSort);
        for (const b of buttons) b.classList.toggle('active', b === button);
        renderProcesses(state.snapshot && state.snapshot.processes);
      });
    }
  }

  // ---------------------------------------------------------------- init

  usageChart.setWindow(state.windowSec);
  networkChart.setWindow(state.windowSec);
  setupRangePicker();
  setupProcSort();
  bootstrap();

  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'visible') {
      scheduleCharts();
    }
  });
})();
