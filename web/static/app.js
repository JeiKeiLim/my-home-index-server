// port-manager dashboard glue: copy-url / copy-cwd / modals / toasts.
// No framework. Event-delegated so newly-swapped htmx rows stay wired
// without re-binding handlers after every poll.

(function () {
  'use strict';

  function readPublicHost() {
    var el = document.getElementById('pm-config');
    if (!el) return window.location.hostname;
    try { return (JSON.parse(el.textContent) || {}).publicHost || window.location.hostname; }
    catch (_) { return window.location.hostname; }
  }
  var PUBLIC_HOST = readPublicHost();

  function $(sel, root) { return (root || document).querySelector(sel); }

  function openModal(id) {
    var el = document.getElementById(id);
    if (el) el.classList.add('active');
  }
  function closeModal(id) {
    var el = document.getElementById(id);
    if (el) el.classList.remove('active');
  }

  function toast(msg, kind) {
    var stack = document.getElementById('toast-stack');
    if (!stack) return;
    var el = document.createElement('div');
    el.className = 'toast ' + (kind || '');
    el.textContent = msg;
    stack.appendChild(el);
    requestAnimationFrame(function () { el.classList.add('show'); });
    setTimeout(function () {
      el.classList.remove('show');
      setTimeout(function () { if (el.parentNode) el.parentNode.removeChild(el); }, 250);
    }, 2000);
  }

  function copyToClipboard(text) {
    if (navigator.clipboard && navigator.clipboard.writeText) {
      return navigator.clipboard.writeText(text);
    }
    // Fallback for browsers without async clipboard API.
    return new Promise(function (resolve, reject) {
      var ta = document.createElement('textarea');
      ta.value = text;
      ta.setAttribute('readonly', '');
      ta.style.position = 'fixed';
      ta.style.opacity = '0';
      document.body.appendChild(ta);
      ta.select();
      try {
        document.execCommand('copy');
        resolve();
      } catch (e) { reject(e); }
      finally { document.body.removeChild(ta); }
    });
  }

  function publicUrl(port) {
    return 'http://' + PUBLIC_HOST + ':' + port + '/';
  }

  // Event delegation — single click handler for the whole document.
  document.addEventListener('click', function (e) {
    var t = e.target;
    if (!(t instanceof HTMLElement)) return;
    if (t.classList.contains('disabled')) { e.preventDefault(); return; }

    var port = t.getAttribute('data-copy-url');
    if (port) {
      var url = publicUrl(port);
      copyToClipboard(url).then(
        function () { toast('copied: ' + url, 'info'); },
        function () { toast('copy failed', 'danger'); }
      );
      return;
    }

    var cwd = t.getAttribute('data-copy-cwd');
    if (cwd) {
      copyToClipboard(cwd).then(
        function () { toast('copied: ' + cwd, 'info'); },
        function () { toast('copy failed', 'danger'); }
      );
      return;
    }

    var visit = t.getAttribute('data-visit');
    if (visit) {
      window.open(publicUrl(visit), '_blank', 'noopener');
      return;
    }

    var killPort = t.getAttribute('data-kill');
    if (killPort) {
      $('#kill-port').textContent = killPort;
      $('#kill-body').innerHTML = '';
      var bodyKill = $('#kill-body');
      var label = t.getAttribute('data-label') || '(no label)';
      var cmd = t.getAttribute('data-cmd') || '';
      var kCwd = t.getAttribute('data-cwd') || '';
      var line1 = document.createElement('div'); line1.className = 'modal-meta-label'; line1.textContent = label;
      var line2 = document.createElement('div'); line2.textContent = cmd;
      var line3 = document.createElement('div'); line3.className = 'modal-meta-dim'; line3.textContent = kCwd;
      bodyKill.appendChild(line1); bodyKill.appendChild(line2); bodyKill.appendChild(line3);
      $('#kill-confirm').setAttribute('data-port', killPort);
      openModal('modal-kill');
      return;
    }

    var renamePort = t.getAttribute('data-rename');
    if (renamePort) {
      $('#rename-input').value = t.getAttribute('data-label') || '';
      $('#rename-confirm').setAttribute('data-port', renamePort);
      openModal('modal-rename');
      setTimeout(function () { $('#rename-input').focus(); }, 30);
      return;
    }

    var restartId = t.getAttribute('data-restart');
    if (restartId) {
      // Heading shows the remembered entry's port number ("Restart
      // port 40193?") rather than the opaque ULID. The row's data-port
      // attribute is populated by _ports.html for both alive and
      // remembered rows; fall back to "(unknown)" only when the
      // remembered entry was captured without a known port (rare).
      var row = t.closest('tr, .card');
      var restartPort = row && row.getAttribute('data-port');
      if (!restartPort || restartPort === '0') restartPort = '(unknown)';
      $('#restart-port').textContent = restartPort;
      $('#restart-body').innerHTML = '';
      $('#restart-confirm').setAttribute('data-id', restartId);
      openModal('modal-restart');
      return;
    }

    var modalClose = t.getAttribute('data-modal-close');
    if (modalClose) { closeModal(modalClose); return; }

    if (t.id === 'kill-confirm') {
      var port = t.getAttribute('data-port');
      mutate('POST', '/kill/' + port).then(
        function () { toast('kill submitted for port ' + port, 'danger'); refresh(); },
        function (err) { toast('kill failed: ' + err, 'danger'); }
      );
      closeModal('modal-kill');
      return;
    }

    if (t.id === 'restart-confirm') {
      var id = t.getAttribute('data-id');
      mutate('POST', '/restart/' + id).then(
        function () { toast('restart submitted', 'magenta'); refresh(); },
        function (err) { toast('restart failed: ' + err, 'danger'); }
      );
      closeModal('modal-restart');
      return;
    }

    if (t.id === 'rename-confirm') {
      var rport = t.getAttribute('data-port');
      var label = $('#rename-input').value.trim();
      mutate('POST', '/rename/' + rport, 'label=' + encodeURIComponent(label),
        'application/x-www-form-urlencoded').then(
        function () { toast('label saved: ' + (label || '(cleared)'), 'info'); refresh(); },
        function (err) { toast('rename failed: ' + err, 'danger'); }
      );
      closeModal('modal-rename');
      return;
    }
  });

  // Close modals on overlay click (but not on inner-modal click).
  document.addEventListener('click', function (e) {
    var t = e.target;
    if (t instanceof HTMLElement && t.classList.contains('overlay')) {
      t.classList.remove('active');
    }
  });

  // Esc closes any open modal; Enter inside rename submits.
  document.addEventListener('keydown', function (e) {
    if (e.key === 'Escape') {
      var actives = document.querySelectorAll('.overlay.active');
      actives.forEach(function (el) { el.classList.remove('active'); });
    }
    if (e.key === 'Enter' && document.activeElement && document.activeElement.id === 'rename-input') {
      var btn = document.getElementById('rename-confirm');
      if (btn) btn.click();
    }
  });

  // Filter (text + source) — purely client-side; toggles hidden class.
  var filterMode = null;
  var filterText = '';
  function applyFilter() {
    var rows = document.querySelectorAll('#ports-body tr, #rows-mobile .card');
    rows.forEach(function (row) {
      var text = (row.textContent || '').toLowerCase();
      var matchesText = !filterText || text.indexOf(filterText) >= 0;
      var matchesMode = true;
      if (filterMode === 'external') matchesMode = text.indexOf('external') >= 0;
      if (filterMode === 'captured') matchesMode = text.indexOf('captured') >= 0;
      row.classList.toggle('row-hidden', !(matchesText && matchesMode));
    });
  }
  document.addEventListener('input', function (e) {
    if (e.target && e.target.id === 'filter-input') {
      filterText = (e.target.value || '').toLowerCase();
      applyFilter();
    }
  });
  document.addEventListener('click', function (e) {
    var btn = e.target;
    if (!(btn instanceof HTMLElement)) return;
    var f = btn.getAttribute('data-filter');
    if (!f) return;
    filterMode = (filterMode === f) ? null : f;
    document.querySelectorAll('[data-filter]').forEach(function (b) {
      b.classList.toggle('primary', b.getAttribute('data-filter') === filterMode);
    });
    applyFilter();
  });

  // After each htmx swap, drain any <template>-wrapped out-of-band payloads.
  // The /ports fragment ships desktop <tr> rows followed by a <template>
  // holding the mobile <div id="rows-mobile" hx-swap-oob>. The <template>
  // wrapper keeps HTML5 foster-parenting from hoisting the <div> out of
  // <tbody> during parsing — here we manually perform the OOB swap that
  // full htmx would do natively.
  function drainOOBTemplates(root) {
    if (!root || !root.querySelectorAll) return;
    var tpls = root.querySelectorAll('template');
    for (var i = 0; i < tpls.length; i++) {
      var tpl = tpls[i];
      if (!tpl.content) continue;
      var oobs = tpl.content.querySelectorAll('[hx-swap-oob]');
      for (var j = 0; j < oobs.length; j++) {
        var el = oobs[j];
        var id = el.getAttribute('id');
        if (!id) continue;
        var live = document.getElementById(id);
        if (!live) continue;
        el.removeAttribute('hx-swap-oob');
        live.replaceWith(el);
      }
      if (tpl.parentNode) tpl.parentNode.removeChild(tpl);
    }
  }

  // Re-apply the filter after htmx swaps in fresh rows.
  document.body.addEventListener('htmx:afterSwap', function (evt) {
    var swapRoot = (evt && evt.target) || document;
    drainOOBTemplates(swapRoot);
    applyFilter();
  });
  document.body.addEventListener('htmx:afterRequest', function (evt) {
    var x = evt.detail && evt.detail.xhr;
    if (x && x.getResponseHeader && x.getResponseHeader('X-Scan-Error') === 'timeout') {
      toast('scan timed out — showing last snapshot', 'danger');
    }
  });

  // Surface htmx auth-bounce. The server uses HX-Redirect on /logout.
  document.body.addEventListener('htmx:beforeOnLoad', function (evt) {
    var x = evt.detail && evt.detail.xhr;
    if (!x) return;
    var loc = x.getResponseHeader('HX-Redirect');
    if (loc) { window.location.href = loc; }
  });

  function mutate(method, url, body, contentType) {
    return new Promise(function (resolve, reject) {
      var x = new XMLHttpRequest();
      x.open(method, url);
      x.setRequestHeader('X-Requested-With', 'XMLHttpRequest');
      if (contentType) x.setRequestHeader('Content-Type', contentType);
      x.onload = function () {
        if (x.status >= 200 && x.status < 400) resolve(x.responseText);
        else reject(x.status + ' ' + x.statusText);
      };
      x.onerror = function () { reject('network error'); };
      x.send(body || null);
    });
  }

  function refresh() {
    var tbody = document.getElementById('ports-body');
    if (tbody && window.htmx) {
      window.htmx.trigger(tbody, 'refresh');
    }
  }
})();
