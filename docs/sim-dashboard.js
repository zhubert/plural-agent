// Dashboard simulation for erg docs pages.
// Shared between index.html and dashboard.html.
// Expects two DOM elements: #simSummary and #simContent.
(function () {
  'use strict';

  var TOKENS_PER_DOLLAR_INPUT = 180000;
  var TOKENS_PER_DOLLAR_OUTPUT = 52000;

  var esc = function(s) {
    var d = document.createElement('div');
    d.textContent = s;
    return d.innerHTML;
  };

  var formatCost = function(v) {
    if (v < 0.01) return '$' + v.toFixed(3);
    return '$' + v.toFixed(2);
  };

  var formatTokens = function(n) {
    if (n >= 1000000) return (n/1000000).toFixed(1) + 'M';
    if (n >= 1000) return (n/1000).toFixed(0) + 'K';
    return '' + n;
  };

  // Log lines that stream in for active items
  var logBank = [
    { type: 'text', text: 'Reading issue description and requirements...' },
    { type: 'tool', text: 'Read: workflow.yaml' },
    { type: 'text', text: 'Analyzing existing codebase for relevant patterns' },
    { type: 'tool', text: 'Glob: **/*.go' },
    { type: 'tool', text: 'Grep: func.*Handler' },
    { type: 'text', text: 'Found 3 relevant handler files to modify' },
    { type: 'tool', text: 'Read: server.go' },
    { type: 'tool', text: 'Read: routes.go' },
    { type: 'text', text: 'Implementing the new endpoint with validation' },
    { type: 'tool', text: 'Edit: handler.go' },
    { type: 'tool', text: 'Edit: handler_test.go' },
    { type: 'text', text: 'Adding table-driven tests for edge cases' },
    { type: 'tool', text: 'Bash: go test ./internal/api/...' },
    { type: 'text', text: 'All 12 tests passing' },
    { type: 'tool', text: 'Edit: openapi.yaml' },
    { type: 'text', text: 'Updated API spec with new endpoint documentation' },
    { type: 'tool', text: 'Bash: go vet ./...' },
    { type: 'text', text: 'No issues found. Preparing commit.' },
    { type: 'tool', text: 'Bash: git add -A && git commit -m "feat: ..."' },
    { type: 'text', text: 'Changes committed and pushed to branch' },
  ];

  // Simulation state — two daemons
  var state = {
    uptime: 1847,
    daemons: [
      {
        repo: 'acme/frontend',
        pid: 48201,
        slots: 3,
        items: [
          { id: 'fe-42', title: '#42 Add dark mode toggle to settings page', state: 'active', step: 'coding', phase: 'sync', cost: 0.48, age: '3m', feedback: 0, logs: [], logIdx: 0 },
          { id: 'fe-38', title: '#38 Fix responsive nav on mobile', state: 'completed', step: 'done', phase: 'idle', cost: 0.31, age: '18m', feedback: 1, pr: '#204' },
          { id: 'fe-45', title: '#45 Migrate CSS modules to Tailwind', state: 'queued', step: 'queued', phase: 'idle', cost: 0, age: '1m', feedback: 0 },
        ]
      },
      {
        repo: 'acme/api',
        pid: 48215,
        slots: 2,
        items: [
          { id: 'api-17', title: '#17 Add rate limiting middleware', state: 'active', step: 'wait_for_ci', phase: 'async_pending', cost: 0.62, age: '7m', feedback: 0, pr: '#89', logs: [], logIdx: 8 },
          { id: 'api-12', title: '#12 Refactor auth token refresh logic', state: 'failed', step: 'coding', phase: 'idle', cost: 0.25, age: '22m', feedback: 0, error: 'go test: FAIL TestRefreshExpired \u2014 context deadline exceeded' },
        ]
      }
    ]
  };

  // Timeline of events to play through
  var timeline = [
    { at: 3, fn: function() { streamLog('fe-42', 2); } },
    { at: 5, fn: function() { streamLog('fe-42', 2); } },
    { at: 7, fn: function() { streamLog('fe-42', 2); tickCost('fe-42', 0.03); } },
    { at: 9, fn: function() {
      setItem('fe-42', { step: 'wait_for_ci', phase: 'async_pending', age: '4m' });
      streamLog('fe-42', 1);
    }},
    { at: 11, fn: function() { tickCost('fe-42', 0.02); } },
    { at: 12, fn: function() {
      setItem('fe-45', { state: 'active', step: 'coding', phase: 'sync', age: '2m' });
    }},
    { at: 14, fn: function() {
      streamLog('fe-45', 2);
      // Demo: simulate clicking Stop on fe-42
      setItem('fe-42', { state: 'failed', step: 'stopped', phase: 'idle' });
      simFlash('fe-42', 'stop', true, 'stopped');
    }},
    { at: 16, fn: function() { streamLog('fe-45', 2); tickCost('fe-45', 0.04); } },
    { at: 18, fn: function() { tickCost('fe-45', 0.02); } },
    { at: 20, fn: function() {
      streamLog('fe-45', 2); tickCost('fe-45', 0.03);
      // Demo: simulate clicking Retry on fe-42
      setItem('fe-42', { state: 'active', step: 'coding', phase: 'sync', error: null, logs: [], logIdx: 0, age: '6m' });
      simFlash('fe-42', 'retry', true, 'queued');
    }},
    { at: 22, fn: function() {
      setItem('api-12', { state: 'active', step: 'coding', phase: 'sync', error: null, age: '24m' });
    }},
    { at: 24, fn: function() { streamLog('fe-45', 2); streamLog('api-12', 2); tickCost('fe-45', 0.02); } },
    { at: 26, fn: function() {
      setItem('api-17', { state: 'completed', step: 'done', phase: 'idle', age: '10m' });
    }},
    { at: 28, fn: function() { streamLog('fe-45', 2); streamLog('api-12', 2); tickCost('api-12', 0.04); } },
    { at: 30, fn: function() {
      setItem('fe-45', { step: 'wait_for_review', phase: 'async_pending', pr: '#209', age: '5m' });
      tickCost('fe-45', 0.02);
    }},
    { at: 32, fn: function() {
      setItem('api-12', { state: 'completed', step: 'done', phase: 'idle', pr: '#91', age: '27m' });
    }},
    { at: 35, fn: function() { resetState(); } },
  ];

  var elapsed = 0;
  var timelineIdx = 0;
  var expandedId = 'fe-42'; // Start with #42 expanded to show logs
  var msgPanelOpen = null;  // item id with message panel open
  var flashData = {};       // id -> {action, text, ok}

  function findItem(id) {
    for (var d = 0; d < state.daemons.length; d++) {
      for (var i = 0; i < state.daemons[d].items.length; i++) {
        if (state.daemons[d].items[i].id === id)
          return { item: state.daemons[d].items[i], daemon: state.daemons[d] };
      }
    }
    return null;
  }

  function setItem(id, props) {
    var found = findItem(id);
    if (!found) return;
    for (var k in props) { found.item[k] = props[k]; }
  }

  function tickCost(id, amount) {
    var found = findItem(id);
    if (!found) return;
    found.item.cost += amount;
  }

  function streamLog(id, count) {
    var found = findItem(id);
    if (!found || !found.item.logs) return;
    var item = found.item;
    for (var i = 0; i < count; i++) {
      item.logs.push(logBank[item.logIdx % logBank.length]);
      item.logIdx++;
      if (item.logs.length > 12) item.logs.shift();
    }
  }

  function resetState() {
    elapsed = 0;
    timelineIdx = 0;
    state.uptime = 1847;
    state.daemons[0].items[0] = { id: 'fe-42', title: '#42 Add dark mode toggle to settings page', state: 'active', step: 'coding', phase: 'sync', cost: 0.48, age: '3m', feedback: 0, logs: [], logIdx: 0 };
    state.daemons[0].items[1] = { id: 'fe-38', title: '#38 Fix responsive nav on mobile', state: 'completed', step: 'done', phase: 'idle', cost: 0.31, age: '18m', feedback: 1, pr: '#204' };
    state.daemons[0].items[2] = { id: 'fe-45', title: '#45 Migrate CSS modules to Tailwind', state: 'queued', step: 'queued', phase: 'idle', cost: 0, age: '1m', feedback: 0 };
    state.daemons[1].items[0] = { id: 'api-17', title: '#17 Add rate limiting middleware', state: 'active', step: 'wait_for_ci', phase: 'async_pending', cost: 0.62, age: '7m', feedback: 0, pr: '#89', logs: [], logIdx: 8 };
    state.daemons[1].items[1] = { id: 'api-12', title: '#12 Refactor auth token refresh logic', state: 'failed', step: 'coding', phase: 'idle', cost: 0.25, age: '22m', feedback: 0, error: 'go test: FAIL TestRefreshExpired \u2014 context deadline exceeded' };
    expandedId = 'fe-42';
    msgPanelOpen = null;
    flashData = {};
  }

  // Compute daemon-level totals from items (derived, not stored)
  function daemonTotals(dm) {
    var cost = 0, inTok = 0, outTok = 0;
    for (var i = 0; i < dm.items.length; i++) {
      cost += dm.items[i].cost;
      inTok += Math.floor(dm.items[i].cost * TOKENS_PER_DOLLAR_INPUT);
      outTok += Math.floor(dm.items[i].cost * TOKENS_PER_DOLLAR_OUTPUT);
    }
    return { cost: cost, inputTokens: inTok, outputTokens: outTok };
  }

  function render() {
    renderSummary();
    renderDaemons();
  }

  function renderSummary() {
    var bar = document.getElementById('simSummary');
    var active = 0, queued = 0, completed = 0, failed = 0;
    var totalCost = 0, totalTokens = 0;
    for (var d = 0; d < state.daemons.length; d++) {
      var totals = daemonTotals(state.daemons[d]);
      totalCost += totals.cost;
      totalTokens += totals.inputTokens + totals.outputTokens;
      for (var i = 0; i < state.daemons[d].items.length; i++) {
        switch (state.daemons[d].items[i].state) {
          case 'active': active++; break;
          case 'queued': queued++; break;
          case 'completed': completed++; break;
          case 'failed': failed++; break;
        }
      }
    }
    bar.innerHTML =
      '<div class="sim-stat"><div class="sl">Daemons</div><div class="sv accent">' + state.daemons.length + '</div></div>' +
      '<div class="sim-stat"><div class="sl">Active</div><div class="sv green">' + active + '</div></div>' +
      '<div class="sim-stat"><div class="sl">Queued</div><div class="sv">' + queued + '</div></div>' +
      '<div class="sim-stat"><div class="sl">Completed</div><div class="sv cyan">' + completed + '</div></div>' +
      (failed > 0 ? '<div class="sim-stat"><div class="sl">Failed</div><div class="sv red">' + failed + '</div></div>' : '') +
      '<div class="sim-stat"><div class="sl">Spend</div><div class="sv">' + formatCost(totalCost) + '</div></div>' +
      '<div class="sim-stat"><div class="sl">Tokens</div><div class="sv">' + formatTokens(totalTokens) + '</div></div>';
  }

  function renderDaemons() {
    var el = document.getElementById('simContent');
    var html = '';
    for (var d = 0; d < state.daemons.length; d++) {
      html += renderDaemon(state.daemons[d]);
    }
    el.innerHTML = html;
    // Attach click and keyboard handlers with accessibility attributes
    el.querySelectorAll('.sim-item[data-id]').forEach(function(card) {
      card.setAttribute('role', 'button');
      card.setAttribute('tabindex', '0');
      card.setAttribute('aria-expanded', expandedId === card.dataset.id ? 'true' : 'false');
      card.addEventListener('click', function(e) {
        if (e.target.tagName === 'A' || e.target.tagName === 'BUTTON') return;
        if (e.target.closest('.sim-ctrl-btns, .sim-msg-panel')) return;
        expandedId = (expandedId === card.dataset.id) ? null : card.dataset.id;
        render();
      });
      card.addEventListener('keydown', function(e) {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault();
          expandedId = (expandedId === card.dataset.id) ? null : card.dataset.id;
          render();
        }
      });
    });
    // Wire control button handlers
    el.querySelectorAll('[data-sim-stop]').forEach(function(btn) {
      btn.addEventListener('click', function(e) { e.stopPropagation(); simStop(btn.dataset.simStop); });
    });
    el.querySelectorAll('[data-sim-retry]').forEach(function(btn) {
      btn.addEventListener('click', function(e) { e.stopPropagation(); simRetry(btn.dataset.simRetry); });
    });
    el.querySelectorAll('[data-sim-open]').forEach(function(btn) {
      btn.addEventListener('click', function(e) { e.stopPropagation(); simOpenMsg(btn.dataset.simOpen); });
    });
    el.querySelectorAll('[data-sim-send]').forEach(function(btn) {
      btn.addEventListener('click', function(e) { e.stopPropagation(); simSendMsg(btn.dataset.simSend); });
    });
    el.querySelectorAll('[data-sim-cancel]').forEach(function(btn) {
      btn.addEventListener('click', function(e) { e.stopPropagation(); simCancelMsg(btn.dataset.simCancel); });
    });
    // Apply pending flashes (after re-render we re-trigger the animation)
    for (var fid in flashData) {
      var fd = flashData[fid];
      var flashEl = document.getElementById('sim-flash-' + fid + '-' + fd.action);
      if (flashEl) {
        flashEl.textContent = fd.ok ? ('✓ ' + fd.text) : ('✗ ' + fd.text);
        flashEl.className = 'sim-flash ' + (fd.ok ? 'ok' : 'err');
        flashEl.style.animation = 'none';
        (function(el) {
          requestAnimationFrame(function() { el.style.animation = ''; });
        })(flashEl);
      }
    }
    // Auto-scroll log viewers to bottom
    el.querySelectorAll('.sim-logs').forEach(function(lv) {
      lv.scrollTop = lv.scrollHeight;
    });
  }

  function renderDaemon(dm) {
    var name = dm.repo.split('/').slice(-2).join('/');
    var upSec = state.uptime;
    var upStr = Math.floor(upSec / 3600) + 'h ' + Math.floor((upSec % 3600) / 60) + 'm';
    var totals = daemonTotals(dm);
    var html = '<div class="sim-daemon">';
    html += '<div class="sim-daemon-hdr"><h4>' + esc(name) + '</h4>';
    html += '<div class="sim-daemon-meta"><span>PID ' + dm.pid + '</span><span>up ' + upStr + '</span><span>slots ' + dm.slots + '</span><span>' + formatCost(totals.cost) + '</span></div>';
    html += '</div><div class="sim-items">';
    // Sort: active first, queued, completed, failed
    var order = { active: 0, queued: 1, completed: 2, failed: 3 };
    var sorted = dm.items.slice().sort(function(a, b) {
      return (order[a.state] || 9) - (order[b.state] || 9);
    });
    for (var i = 0; i < sorted.length; i++) {
      html += renderItem(sorted[i]);
    }
    html += '</div></div>';
    return html;
  }

  function renderItem(item) {
    var isExp = expandedId === item.id;
    var pulseCls = (item.state === 'active' && item.phase === 'async_pending') ? ' pulsing' : '';
    var html = '<div class="sim-item st-' + item.state + pulseCls + '" data-id="' + esc(item.id) + '">';
    html += '<div class="sim-item-top"><div class="sim-item-title">' + esc(item.title) + '</div>';
    html += '<span class="sim-badge ' + item.state + '">' + item.state + '</span></div>';
    html += '<div class="sim-item-meta">';
    html += '<span><span class="ml">step</span> <span class="mv step">' + esc(item.step) + '</span></span>';
    html += '<span><span class="ml">phase</span> <span class="mv phase">' + esc(item.phase) + '</span></span>';
    html += '<span><span class="ml">age</span> <span class="mv">' + esc(item.age) + '</span></span>';
    if (item.cost > 0) html += '<span><span class="ml">cost</span> <span class="mv">' + formatCost(item.cost) + '</span></span>';
    if (item.feedback > 0) html += '<span><span class="ml">feedback</span> <span class="mv">' + item.feedback + ' rounds</span></span>';
    html += '</div>';
    if (item.pr) html += '<div class="sim-item-pr"><button type="button" class="sim-item-pr-link">PR ' + esc(item.pr) + '</button></div>';
    if (item.error) html += '<div class="sim-item-error">' + esc(item.error) + '</div>';
    // Control buttons
    var isActive = item.state === 'active';
    var isFailed = item.state === 'failed';
    var hasLogs = !!item.logs;
    var isMsgOpen = msgPanelOpen === item.id;
    var ctrlBtns = '';
    if (isActive) ctrlBtns += '<button class="sim-ctrl-btn stop" data-sim-stop="' + esc(item.id) + '">Stop</button>';
    if (isFailed) ctrlBtns += '<button class="sim-ctrl-btn retry" data-sim-retry="' + esc(item.id) + '">Retry</button>';
    if (isActive && hasLogs && !isMsgOpen) ctrlBtns += '<button class="sim-ctrl-btn message" data-sim-open="' + esc(item.id) + '">Send Message</button>';
    var flashSpans = '';
    if (isActive) flashSpans += '<span class="sim-flash" id="sim-flash-' + esc(item.id) + '-stop"></span>';
    if (isFailed) flashSpans += '<span class="sim-flash" id="sim-flash-' + esc(item.id) + '-retry"></span>';
    if (isActive && hasLogs) flashSpans += '<span class="sim-flash" id="sim-flash-' + esc(item.id) + '-message"></span>';
    if (ctrlBtns || isMsgOpen) {
      html += '<div class="sim-ctrl-btns">' + ctrlBtns + flashSpans + '</div>';
    }
    if (isMsgOpen) {
      html += '<div class="sim-msg-panel">' +
        '<textarea id="sim-ta-' + esc(item.id) + '" placeholder="Message to inject into the running session…" rows="3"></textarea>' +
        '<div class="sim-msg-panel-actions">' +
          '<button class="sim-ctrl-btn send" data-sim-send="' + esc(item.id) + '">Send</button>' +
          '<button class="sim-ctrl-btn cancel" data-sim-cancel="' + esc(item.id) + '">Cancel</button>' +
        '</div></div>';
    }
    if (isExp && item.logs && item.logs.length > 0) {
      html += '<div class="sim-logs">';
      for (var l = 0; l < item.logs.length; l++) {
        html += '<div class="ll ' + item.logs[l].type + '">' + esc(item.logs[l].text) + '</div>';
      }
      html += '</div>';
    }
    html += '</div>';
    return html;
  }

  // Simulated control action handlers

  function simFlash(id, action, ok, text) {
    flashData[id] = { action: action, ok: ok, text: text };
    // Clear flash after animation completes
    setTimeout(function() {
      if (flashData[id] && flashData[id].action === action) delete flashData[id];
    }, 2200);
  }

  function simStop(id) {
    setItem(id, { state: 'failed', step: 'stopped', phase: 'idle' });
    simFlash(id, 'stop', true, 'stopped');
    render();
  }

  function simRetry(id) {
    setItem(id, { state: 'active', step: 'coding', phase: 'sync', error: null, logs: [], logIdx: 0 });
    simFlash(id, 'retry', true, 'queued');
    render();
  }

  function simOpenMsg(id) {
    msgPanelOpen = id;
    render();
    var ta = document.getElementById('sim-ta-' + id);
    if (ta) ta.focus();
  }

  function simCancelMsg(id) {
    msgPanelOpen = null;
    render();
  }

  function simSendMsg(id) {
    msgPanelOpen = null;
    simFlash(id, 'message', true, 'sent');
    render();
  }

  // Initial render, then tick every 1.5s
  render();
  setInterval(function () {
    if (document.hidden) return; // skip when tab is backgrounded
    elapsed += 1.5;
    state.uptime += 1.5;
    while (timelineIdx < timeline.length && timeline[timelineIdx].at <= elapsed) {
      timeline[timelineIdx].fn();
      timelineIdx++;
    }
    render();
  }, 1500);
})();
