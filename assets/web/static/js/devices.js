/**
 * devices.js - the devices table.
 *
 * The table is a projection of the fleet state that FleetStream keeps
 * current. Filtering, sorting and pagination run here, over the fleet that
 * is already in the browser; the server only sends changes.
 *
 * Each row is created once and updated cell by cell: a cell is redrawn only
 * when the data it shows changes. That preserves focus, selection and an
 * open <select> when an update arrives.
 */
(function () {
    'use strict';

    window.openEmulatorForm = window.openEmulatorForm || function () {
        window.Toast.err('Emulator form unavailable');
    };

    var LABELS = {
        running: 'running', stopped: 'stopped', disabled: 'disabled',
        error: 'error', starting: 'starting'
    };
    // Sort order for "Status": what needs attention comes first.
    var STATUS_WEIGHT = { error: 0, starting: 1, running: 2, stopped: 3, disabled: 4 };
    var PAGE_SIZES = [10, 25, 50, 100];
    var PAGE_WINDOW = 7;
    // Must match maxBatchSize on the server. Larger selections are sent in
    // consecutive chunks.
    var BATCH_CHUNK = 1000;

    var state = {
        query: '',
        status: '',
        model: '',
        sort: 'id',
        dir: 'asc',
        page: 1,
        perPage: 10,
        selected: new Set(),
        // IDs with an action in flight: the button stays "pending" until
        // the device's next update arrives on the stream.
        pending: new Set(),
        // Freshly created IDs: the row flashes when it appears.
        highlight: new Set(),
        batchRunning: false
    };

    var rows = new Map(); // id -> <tr>
    var visible = [];     // devices on the current page, in order
    var filtered = [];    // every device that passes the filter, sorted

    function el(id) { return document.getElementById(id); }

    function plural(n, word) {
        return n + ' ' + word + (n === 1 ? '' : 's');
    }

    // ------------------------------------------------------------------
    // Effective device status
    // ------------------------------------------------------------------

    // "error" is not a stored status: it is a stopped device whose last
    // start failed. Showing it tells the operator why Start "did nothing".
    function statusOf(d) {
        if (d.status === 'stopped' && d.last_error) { return 'error'; }
        return d.status;
    }

    // ------------------------------------------------------------------
    // Filter, sort, paginate
    // ------------------------------------------------------------------

    function passesFilter(d) {
        if (state.status && statusOf(d) !== state.status) { return false; }
        if (state.model && d.model !== state.model) { return false; }
        if (state.query) {
            var q = state.query.toLowerCase();
            if (String(d.id).indexOf(q) === -1 &&
                String(d.port).indexOf(q) === -1 &&
                (d.name || '').toLowerCase().indexOf(q) === -1) {
                return false;
            }
        }
        return true;
    }

    function sortKey(d) {
        switch (state.sort) {
            case 'name': return (d.name || '').toLowerCase();
            case 'model': return d.model || '';
            case 'port': return d.port;
            case 'users': return d.total_users;
            case 'status': return STATUS_WEIGHT[statusOf(d)];
            default: return d.id;
        }
    }

    function compare(a, b) {
        var ka = sortKey(a);
        var kb = sortKey(b);
        var r = ka < kb ? -1 : ka > kb ? 1 : a.id - b.id;
        return state.dir === 'desc' ? -r : r;
    }

    function recompute() {
        filtered = window.FleetStream.all().filter(passesFilter).sort(compare);

        var pages = Math.max(1, Math.ceil(filtered.length / state.perPage));
        if (state.page > pages) { state.page = pages; }

        var start = (state.page - 1) * state.perPage;
        visible = filtered.slice(start, start + state.perPage);
    }

    // ------------------------------------------------------------------
    // Node builders
    // ------------------------------------------------------------------

    function node(tag, className, text) {
        var n = document.createElement(tag);
        if (className) { n.className = className; }
        if (text !== undefined) { n.textContent = text; }
        return n;
    }

    function icon(name) {
        var svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
        svg.setAttribute('class', 'icon');
        svg.setAttribute('aria-hidden', 'true');
        var use = document.createElementNS('http://www.w3.org/2000/svg', 'use');
        use.setAttribute('href', '/static/icons.svg#' + name);
        svg.appendChild(use);
        return svg;
    }

    function iconButton(className, iconName, title, hiddenLabel) {
        var b = node('button', 'btn btn--sm ' + className);
        b.type = 'button';
        b.title = title;
        b.appendChild(icon(iconName));
        b.appendChild(node('span', 'visually-hidden', hiddenLabel));
        return b;
    }

    function led(st) {
        var l = node('span', 'led');
        l.setAttribute('data-state', st);
        l.setAttribute('aria-hidden', 'true');
        return l;
    }

    // Each column: sig() summarizes what the cell shows; render() draws it.
    // The cell is redrawn only when sig changes.
    var COLUMNS = [
        {
            className: 'center',
            sig: function (d) { return String(state.selected.has(d.id)); },
            render: function (td, d) {
                var c = td.firstChild;
                if (!c) {
                    c = node('input', 'check device-check');
                    c.type = 'checkbox';
                    td.appendChild(c);
                }
                c.setAttribute('aria-label', 'Select ' + d.name);
                c.checked = state.selected.has(d.id);
            }
        },
        {
            sig: function (d) { return statusOf(d) + '|' + d.id; },
            render: function (td, d) {
                var old = td.querySelector('.led');
                var previous = old ? old.getAttribute('data-state') : null;
                td.textContent = '';
                var span = node('span', 'grid__id');
                var l = led(statusOf(d));
                span.appendChild(l);
                span.appendChild(document.createTextNode(' ' + d.id));
                td.appendChild(span);
                if (previous && previous !== statusOf(d)) { flash(l); }
            }
        },
        {
            className: 'grid__name device-name-cell',
            sig: function (d) { return d.name; },
            render: function (td, d) { td.textContent = d.name; td.title = d.name; }
        },
        {
            sig: function (d) { return d.model; },
            render: function (td, d) { td.textContent = d.model; }
        },
        {
            className: 'num',
            sig: function (d) { return String(d.port); },
            render: function (td, d) { td.textContent = d.port; }
        },
        {
            // LocalAuthentication is only read by the Dahua emulator.
            className: 'center',
            sig: function (d) { return d.model + '|' + d.mode; },
            render: function (td, d) {
                if (d.model !== 'Dahua') {
                    td.textContent = '';
                    var dash = node('span', 'text-low', '—');
                    dash.title = 'This model does not use this setting';
                    td.appendChild(dash);
                    return;
                }
                var s = td.querySelector('select');
                if (!s) {
                    td.textContent = '';
                    s = node('select', 'select device-mode');
                    [['online', 'Online'], ['standalone', 'Standalone']].forEach(function (o) {
                        var opt = node('option', '', o[1]);
                        opt.value = o[0];
                        s.appendChild(opt);
                    });
                    td.appendChild(s);
                }
                s.setAttribute('aria-label', 'Mode of ' + d.name);
                s.value = d.mode;
            }
        },
        {
            className: 'center',
            sig: function (d) { return String(d.log_enabled); },
            render: function (td, d) {
                var c = td.firstChild;
                if (!c) {
                    c = node('input', 'check log-check');
                    c.type = 'checkbox';
                    td.appendChild(c);
                }
                c.checked = d.log_enabled === 1;
                c.setAttribute('aria-label', 'Record log for ' + d.name);
                c.title = 'Record event log';
            }
        },
        {
            className: 'num',
            sig: function (d) { return String(d.interval); },
            render: function (td, d) { td.textContent = d.interval + 's'; }
        },
        {
            className: 'num',
            sig: function (d) { return String(d.total_users); },
            render: function (td, d, first) {
                td.textContent = d.total_users.toLocaleString('en-US');
                if (!first) { highlightCell(td); }
            }
        },
        {
            sig: function (d) { return statusOf(d) + '|' + (d.last_error || ''); },
            render: function (td, d) {
                var st = statusOf(d);
                td.textContent = '';
                var b = node('span', 'badge');
                b.setAttribute('data-state', st);
                b.appendChild(led(st));
                b.appendChild(document.createTextNode(' ' + (LABELS[st] || st)));
                if (d.last_error) { b.title = 'Last start failed: ' + d.last_error; }
                td.appendChild(b);
            }
        },
        {
            sig: function (d) {
                return [d.status, state.pending.has(d.id)].join('|');
            },
            render: function (td, d) {
                td.textContent = '';
                var actions = node('div', 'row-actions');
                var pending = state.pending.has(d.id);

                var start = iconButton('btn--go device-start', 'play', 'Start', 'Start ' + d.name);
                start.disabled = d.status !== 'stopped' || pending;
                var stop = iconButton('btn--halt device-stop', 'stop', 'Stop', 'Stop ' + d.name);
                stop.disabled = d.status !== 'running' || pending;
                if (pending) {
                    (d.status === 'running' ? stop : start).setAttribute('data-pending', 'true');
                }
                actions.appendChild(start);
                actions.appendChild(stop);
                actions.appendChild(iconButton('device-details-btn', 'users', 'Users and settings', 'Details of ' + d.name));

                var edit = iconButton('device-edit', 'pencil', 'Edit', 'Edit ' + d.name);
                edit.disabled = d.status !== 'stopped';
                if (edit.disabled) { edit.title = 'Stop the emulator to edit it'; }
                actions.appendChild(edit);
                actions.appendChild(iconButton('btn--halt device-remove', 'close', 'Delete', 'Delete ' + d.name));
                td.appendChild(actions);
            }
        }
    ];

    function flash(target) {
        target.classList.remove('led--flash');
        void target.offsetWidth; // restart the animation on back-to-back changes
        target.classList.add('led--flash');
    }

    function highlightCell(td) {
        td.classList.remove('cell--changed');
        void td.offsetWidth;
        td.classList.add('cell--changed');
    }

    function createRow(d) {
        var tr = node('tr');
        tr.id = 'device-' + d.id;
        tr.setAttribute('data-id', d.id);
        tr._sigs = [];
        COLUMNS.forEach(function (col) {
            tr.appendChild(node('td', col.className || ''));
        });
        updateRow(tr, d, true);
        return tr;
    }

    function updateRow(tr, d, first) {
        tr.setAttribute('data-state', statusOf(d));
        tr.setAttribute('aria-selected', String(state.selected.has(d.id)));
        COLUMNS.forEach(function (col, i) {
            var sig = col.sig(d);
            if (tr._sigs[i] === sig) { return; }
            tr._sigs[i] = sig;
            col.render(tr.children[i], d, first);
        });
    }

    // ------------------------------------------------------------------
    // Render
    // ------------------------------------------------------------------

    function render() {
        recompute();

        var tbody = el('device-rows');
        var used = new Set();

        if (visible.length === 0) {
            tbody.textContent = '';
            tbody.appendChild(emptyRow());
        } else {
            var empty = tbody.querySelector('.grid__empty');
            if (empty) { empty.remove(); }

            visible.forEach(function (d, i) {
                var tr = rows.get(d.id);
                if (tr) {
                    updateRow(tr, d, false);
                } else {
                    tr = createRow(d);
                    rows.set(d.id, tr);
                }
                if (state.highlight.has(d.id)) {
                    state.highlight.delete(d.id);
                    tr.classList.add('row--new');
                }
                used.add(tr);
                // Only move the node when its position changes: moving a
                // <tr> that holds focus drops the focus.
                if (tbody.children[i] !== tr) {
                    tbody.insertBefore(tr, tbody.children[i] || null);
                }
            });

            Array.prototype.slice.call(tbody.children).forEach(function (tr) {
                if (!used.has(tr)) { tr.remove(); }
            });
        }

        renderPagination();
        renderSummary();
        renderSelection();
        renderSorting();
    }

    function emptyRow() {
        var tr = node('tr', 'grid__empty');
        var td = node('td');
        td.colSpan = COLUMNS.length;
        var box = node('div', 'empty');
        var total = window.FleetStream.all().length;

        if (!window.FleetStream.ready) {
            box.appendChild(node('p', 'empty__title', 'Loading devices…'));
        } else if (total === 0) {
            box.appendChild(node('p', 'empty__title', 'No devices yet'));
            box.appendChild(node('p', '', 'Create one emulator, or a range of them, to get started.'));
        } else {
            box.appendChild(node('p', 'empty__title', 'No devices match the filter'));
            var clear = node('button', 'btn btn--action', 'Clear filters');
            clear.type = 'button';
            clear.addEventListener('click', clearFilters);
            box.appendChild(clear);
        }
        td.appendChild(box);
        tr.appendChild(td);
        return tr;
    }

    function renderSummary() {
        var total = window.FleetStream.all().length;
        var target = el('grid-summary');
        if (filtered.length === 0) {
            target.textContent = total === 0 ? '' : '0 of ' + total;
            return;
        }
        var first = (state.page - 1) * state.perPage + 1;
        var last = first + visible.length - 1;
        target.textContent = first + '–' + last + ' of ' + filtered.length +
            (filtered.length !== total ? ' (filtered from ' + total + ')' : '');
    }

    function renderPagination() {
        var nav = el('pager-pages');
        var pages = Math.max(1, Math.ceil(filtered.length / state.perPage));
        nav.textContent = '';

        function link(label, page, ariaLabel) {
            var b = node('button', 'pager__page');
            b.type = 'button';
            if (typeof label === 'string') {
                b.textContent = label;
            } else {
                b.appendChild(label);
            }
            b.disabled = page < 1 || page > pages;
            if (page === state.page && !ariaLabel) { b.setAttribute('aria-current', 'page'); }
            if (ariaLabel) { b.setAttribute('aria-label', ariaLabel); }
            b.addEventListener('click', function () { goToPage(page); });
            nav.appendChild(b);
        }

        link(icon('chevron-left'), state.page - 1, 'Previous page');

        var start = Math.max(1, state.page - Math.floor(PAGE_WINDOW / 2));
        var end = Math.min(pages, start + PAGE_WINDOW - 1);
        start = Math.max(1, end - PAGE_WINDOW + 1);
        for (var p = start; p <= end; p++) { link(String(p), p); }

        link(icon('chevron-right'), state.page + 1, 'Next page');
    }

    function renderSorting() {
        document.querySelectorAll('.grid__sort').forEach(function (b) {
            var th = b.closest('th');
            var active = b.getAttribute('data-sort') === state.sort;
            th.setAttribute('aria-sort', active ? (state.dir === 'asc' ? 'ascending' : 'descending') : 'none');
        });
    }

    function goToPage(page) {
        state.page = page;
        saveToURL();
        render();
        el('device-grid').scrollIntoView({ block: 'nearest' });
    }

    // ------------------------------------------------------------------
    // Selection - persists across pages and filters
    // ------------------------------------------------------------------

    function renderSelection() {
        // Drop from the selection whatever no longer exists.
        state.selected.forEach(function (id) {
            if (!window.FleetStream.get(id)) { state.selected.delete(id); }
        });

        var n = state.selected.size;
        el('bulk-bar').hidden = n === 0;
        el('bulk-count').textContent = n + ' selected';

        var unselectedInFilter = filtered.filter(function (d) {
            return !state.selected.has(d.id);
        }).length;
        var selectFiltered = el('select-filtered');
        selectFiltered.hidden = unselectedInFilter === 0;
        selectFiltered.textContent = filtered.length === window.FleetStream.all().length
            ? 'Select all ' + filtered.length
            : 'Select all ' + filtered.length + ' filtered';

        document.querySelectorAll('#bulk-bar [data-batch]').forEach(function (b) {
            b.disabled = state.batchRunning;
        });

        var onPage = visible.filter(function (d) { return state.selected.has(d.id); }).length;
        var selectAll = el('select-all');
        selectAll.checked = visible.length > 0 && onPage === visible.length;
        selectAll.indeterminate = onPage > 0 && onPage < visible.length;
    }

    function select(ids, checked) {
        ids.forEach(function (id) {
            if (checked) { state.selected.add(id); } else { state.selected.delete(id); }
        });
        render();
    }

    function selectedIDs() {
        return Array.from(state.selected);
    }

    // ------------------------------------------------------------------
    // Filters and URL
    // ------------------------------------------------------------------

    function loadFromURL() {
        var p = new URLSearchParams(window.location.search);
        state.query = p.get('q') || '';
        state.status = p.get('status') || '';
        state.model = p.get('model') || '';
        state.sort = p.get('sort') || 'id';
        state.dir = p.get('dir') === 'desc' ? 'desc' : 'asc';
        state.page = Math.max(1, parseInt(p.get('page'), 10) || 1);
        var pp = parseInt(p.get('per_page'), 10);
        state.perPage = PAGE_SIZES.indexOf(pp) !== -1 ? pp : 10;

        el('filter-q').value = state.query;
        el('filter-status').value = state.status;
        el('filter-model').value = state.model;
        el('per-page').value = String(state.perPage);
    }

    function saveToURL() {
        var p = new URLSearchParams();
        if (state.query) { p.set('q', state.query); }
        if (state.status) { p.set('status', state.status); }
        if (state.model) { p.set('model', state.model); }
        if (state.sort !== 'id') { p.set('sort', state.sort); }
        if (state.dir !== 'asc') { p.set('dir', state.dir); }
        if (state.page !== 1) { p.set('page', String(state.page)); }
        if (state.perPage !== 10) { p.set('per_page', String(state.perPage)); }
        var qs = p.toString();
        window.history.replaceState(null, '', window.location.pathname + (qs ? '?' + qs : ''));
    }

    function applyFilters() {
        state.query = el('filter-q').value.trim();
        state.status = el('filter-status').value;
        state.model = el('filter-model').value;
        state.page = 1;
        saveToURL();
        render();
    }

    function clearFilters() {
        el('filter-form').reset();
        applyFilters();
    }

    function initFilters() {
        var form = el('filter-form');
        var timer = null;

        form.addEventListener('submit', function (e) { e.preventDefault(); });
        form.addEventListener('reset', function () {
            // The browser resets the fields after this event fires.
            window.setTimeout(applyFilters, 0);
        });
        el('filter-q').addEventListener('input', function () {
            window.clearTimeout(timer);
            timer = window.setTimeout(applyFilters, 120);
        });
        ['filter-status', 'filter-model'].forEach(function (id) {
            el(id).addEventListener('change', applyFilters);
        });

        el('per-page').addEventListener('change', function () {
            state.perPage = Number(el('per-page').value);
            state.page = 1;
            saveToURL();
            render();
        });

        document.querySelectorAll('.grid__sort').forEach(function (b) {
            b.addEventListener('click', function () {
                var col = b.getAttribute('data-sort');
                if (state.sort === col) {
                    state.dir = state.dir === 'asc' ? 'desc' : 'asc';
                } else {
                    state.sort = col;
                    state.dir = 'asc';
                }
                saveToURL();
                render();
            });
        });

        // "/" focuses the search box, as in other console tools.
        document.addEventListener('keydown', function (e) {
            if (e.key !== '/' || e.ctrlKey || e.metaKey || e.altKey) { return; }
            var target = e.target;
            if (target.closest('input, textarea, select, dialog[open]')) { return; }
            e.preventDefault();
            el('filter-q').focus();
        });
    }

    // ------------------------------------------------------------------
    // Actions
    // ------------------------------------------------------------------

    function send(url, method, body) {
        return fetch(url, {
            method: method,
            headers: { 'Content-Type': 'application/json' },
            body: body === undefined ? undefined : JSON.stringify(body)
        }).then(function (response) {
            return response.json().catch(function () { return {}; }).then(function (data) {
                if (!response.ok) {
                    throw new Error(data.error || 'HTTP ' + response.status);
                }
                return data;
            });
        });
    }

    // Start or stop one device from its row.
    function controlOne(d, action) {
        state.pending.add(d.id);
        render();

        send('/api/devices/' + d.id + '/' + action, 'POST')
            .then(function () {
                window.Toast.ok('Emulator ' + d.name + (action === 'start' ? ' started' : ' stopped'));
            })
            .catch(function (err) {
                window.Toast.err('Could not ' + action + ' ' + d.name + ': ' + err.message);
            })
            .then(function () {
                // The final state arrives on the stream; this only releases
                // the button.
                state.pending.delete(d.id);
                render();
            });
    }

    function deleteOne(d, button) {
        if (!window.confirm('Delete emulator "' + d.name + '"?\n\n' +
            'Cards, faces and users stored on it are deleted too. This cannot be undone.')) {
            return;
        }
        button.disabled = true;
        send('/api/emulators/' + d.id, 'DELETE')
            .then(function () { window.Toast.ok('Emulator ' + d.name + ' deleted'); })
            .catch(function (err) {
                window.Toast.err(err.message);
                button.disabled = false;
            });
    }

    // Batch actions. Past tense and progress verb for the toasts.
    var BATCH_VERBS = {
        start: { past: 'Started', doing: 'Starting' },
        stop: { past: 'Stopped', doing: 'Stopping' },
        enable: { past: 'Enabled', doing: 'Enabling' },
        disable: { past: 'Disabled', doing: 'Disabling' },
        'delete': { past: 'Deleted', doing: 'Deleting' }
    };

    function chunks(ids, size) {
        var out = [];
        for (var i = 0; i < ids.length; i += size) { out.push(ids.slice(i, i + size)); }
        return out;
    }

    // Sends the batch in chunks of BATCH_CHUNK, one after another, and
    // merges the per-device results.
    function sendBatch(action, ids) {
        var total = { requested: 0, succeeded: 0, failed: 0, skipped: 0, results: [] };
        return chunks(ids, BATCH_CHUNK).reduce(function (prev, chunk) {
            return prev.then(function () {
                return send('/api/emulators/batch', 'POST', { ids: chunk, action: action })
                    .then(function (data) {
                        total.requested += data.requested;
                        total.succeeded += data.succeeded;
                        total.failed += data.failed;
                        total.skipped += data.skipped;
                        total.results = total.results.concat(data.results || []);
                    });
            });
        }, Promise.resolve()).then(function () { return total; });
    }

    function confirmBatch(action, ids) {
        if (action === 'delete') {
            return window.confirm('Delete ' + plural(ids.length, 'emulator') + '?\n\n' +
                'Cards, faces and users stored on them are deleted too. This cannot be undone.');
        }
        if (action === 'disable') {
            var running = ids.filter(function (id) {
                var d = window.FleetStream.get(id);
                return d && d.status === 'running';
            }).length;
            if (running > 0) {
                return window.confirm(plural(running, 'selected emulator') +
                    (running === 1 ? ' is' : ' are') + ' running and will be stopped. Continue?');
            }
        }
        return true;
    }

    function reportBatch(action, summary) {
        var verb = BATCH_VERBS[action];
        var done = summary.succeeded - summary.skipped;
        var message = verb.past + ' ' + done + ' of ' + plural(summary.requested, 'emulator');
        if (summary.skipped > 0) {
            message += ' (' + summary.skipped + ' already ' +
                (action === 'start' ? 'running' : action === 'stop' ? 'stopped' : action + 'd') + ')';
        }

        if (summary.failed === 0) {
            window.Toast.ok(message);
            return;
        }
        var firstFailure = summary.results.filter(function (r) { return !r.ok; })[0];
        window.Toast.err(message + '. ' + summary.failed + ' failed' +
            (firstFailure ? ', e.g. #' + firstFailure.id + ': ' + firstFailure.error : ''));
    }

    function runBatch(action) {
        var ids = selectedIDs();
        if (ids.length === 0 || state.batchRunning) { return; }
        if (!confirmBatch(action, ids)) { return; }

        state.batchRunning = true;
        if (action === 'start' || action === 'stop') {
            ids.forEach(function (id) { state.pending.add(id); });
        }
        render();
        if (ids.length > 50) {
            window.Toast.info(BATCH_VERBS[action].doing + ' ' + plural(ids.length, 'emulator') + '…');
        }

        sendBatch(action, ids)
            .then(function (summary) {
                reportBatch(action, summary);
                if (action === 'delete') {
                    summary.results.forEach(function (r) {
                        if (r.ok) { state.selected.delete(r.id); }
                    });
                }
            })
            .catch(function (err) {
                window.Toast.err('Batch ' + action + ' failed: ' + err.message);
            })
            .then(function () {
                state.batchRunning = false;
                ids.forEach(function (id) { state.pending.delete(id); });
                render();
            });
    }

    function rowID(target) {
        var tr = target.closest('tr[data-id]');
        return tr ? Number(tr.getAttribute('data-id')) : null;
    }

    function initActions() {
        var tbody = el('device-rows');

        // Delegation: rows come and go with the stream, so no listener is
        // bound to a specific button.
        tbody.addEventListener('click', function (e) {
            var target = e.target.closest('button');
            if (!target) { return; }
            var id = rowID(target);
            var d = window.FleetStream.get(id);
            if (!d) { return; }

            if (target.classList.contains('device-start')) {
                controlOne(d, 'start');
            } else if (target.classList.contains('device-stop')) {
                controlOne(d, 'stop');
            } else if (target.classList.contains('device-details-btn') && window.DeviceDrawer) {
                window.DeviceDrawer.open(id, d.name);
            } else if (target.classList.contains('device-edit')) {
                window.openEmulatorForm({
                    id: d.id, name: d.name, model: d.model, port: d.port,
                    ip_address: d.ip_address, event_interval: d.interval,
                    enabled: d.enabled === 1
                });
            } else if (target.classList.contains('device-remove')) {
                deleteOne(d, target);
            }
        });

        tbody.addEventListener('change', function (e) {
            var target = e.target;
            var id = rowID(target);
            if (id === null) { return; }

            if (target.classList.contains('device-check')) {
                select([id], target.checked);
            } else if (target.classList.contains('log-check')) {
                toggleLog(target, id);
            } else if (target.classList.contains('device-mode')) {
                changeMode(target, id);
            }
        });

        el('select-all').addEventListener('change', function (e) {
            select(visible.map(function (d) { return d.id; }), e.target.checked);
        });
        el('select-filtered').addEventListener('click', function () {
            select(filtered.map(function (d) { return d.id; }), true);
        });
        el('clear-selection').addEventListener('click', function () {
            state.selected.clear();
            render();
        });

        document.querySelectorAll('#bulk-bar [data-batch]').forEach(function (b) {
            b.addEventListener('click', function () { runBatch(b.getAttribute('data-batch')); });
        });
    }

    // The log flag is saved immediately, whether the emulator is running
    // or not.
    function toggleLog(target, id) {
        var on = target.checked;
        if (!on && !window.confirm('Turning the log off deletes this emulator\'s log files. Continue?')) {
            target.checked = true;
            return;
        }
        target.disabled = true;
        send('/api/devices/' + id + '/settings', 'PUT', { log_enabled: on })
            .then(function () {
                window.Toast.ok('Log ' + (on ? 'on' : 'off') + ' for device ' + id);
            })
            .catch(function (err) {
                target.checked = !on;
                window.Toast.err('Could not change the log setting: ' + err.message);
            })
            .then(function () { target.disabled = false; });
    }

    function changeMode(target, id) {
        var d = window.FleetStream.get(id);
        var previous = d ? d.mode : target.value;
        send('/api/devices/' + id + '/mode', 'POST', { mode: target.value })
            .then(function () { window.Toast.ok('Device ' + id + ': mode ' + target.value); })
            .catch(function () {
                // Revert the select: keeping a value that was not saved
                // would make the screen lie about the device.
                target.value = previous;
                window.Toast.err('Could not change the mode of device ' + id);
            });
    }

    // ------------------------------------------------------------------
    // Reachability notice
    // ------------------------------------------------------------------

    var reachabilityTimer = null;

    // Re-evaluates the notice. It disappears on its own once the problem
    // is gone.
    function loadReachability() {
        var row = el('reachability-alert-row');
        if (!row) { return; }

        fetch('/api/reachability')
            .then(function (response) {
                if (!response.ok) { throw new Error('HTTP ' + response.status); }
                return response.json();
            })
            .then(function (report) {
                var problems = (report.devices || []).filter(function (d) {
                    return d.status === 'unreachable' || d.status === 'unknown';
                });
                if (problems.length === 0) {
                    row.hidden = true;
                    return;
                }

                el('reachability-headline').textContent =
                    plural(problems.length, 'device') + ' will not be reachable from outside this host';
                el('reachability-reason').textContent = problems[0].reason || '';
                el('reachability-help').textContent = 'See "Ports and networking" in the docs.';

                var list = el('reachability-list');
                list.textContent = '';
                problems.forEach(function (d) {
                    list.appendChild(node('li', '', 'Device ' + d.device_id + ', port ' + d.port +
                        (d.reason ? ': ' + d.reason : '')));
                });
                row.hidden = false;
            })
            .catch(function () { /* no reliable answer: leave the notice as is */ });
    }

    function scheduleReachability() {
        window.clearTimeout(reachabilityTimer);
        reachabilityTimer = window.setTimeout(loadReachability, 2000);
    }

    function initReachability() {
        var toggle = el('reachability-toggle');
        if (toggle) {
            toggle.addEventListener('click', function () {
                var list = el('reachability-list');
                list.hidden = !list.hidden;
                toggle.setAttribute('aria-expanded', String(!list.hidden));
                toggle.textContent = list.hidden ? 'Show list' : 'Hide list';
            });
        }
        loadReachability();
    }

    // ------------------------------------------------------------------
    // Stream
    // ------------------------------------------------------------------

    var renderScheduled = false;

    // Several updates in the same frame collapse into one render.
    function scheduleRender() {
        if (renderScheduled) { return; }
        renderScheduled = true;
        window.requestAnimationFrame(function () {
            renderScheduled = false;
            render();
        });
    }

    function initStream() {
        window.FleetStream.subscribe('snapshot', function () {
            // Cached rows may belong to devices that no longer exist.
            rows.forEach(function (tr, id) {
                if (!window.FleetStream.get(id)) { rows.delete(id); }
            });
            scheduleRender();
        });

        window.FleetStream.subscribe('delta', function (data) {
            var statusChanged = false;
            data.devices.forEach(function (d) {
                var before = data.previous[d.id];
                if (!before || before.status !== d.status) { statusChanged = true; }
            });
            data.removed.forEach(function (id) {
                rows.delete(id);
                state.selected.delete(id);
                statusChanged = true;
            });
            scheduleRender();
            if (statusChanged) { scheduleReachability(); }
        });
    }

    // Called by the form after creating: the new rows flash when they
    // appear.
    window.DevicesTable = {
        highlight: function (ids) {
            (ids || []).forEach(function (id) { state.highlight.add(Number(id)); });
            scheduleRender();
        }
    };

    document.addEventListener('DOMContentLoaded', function () {
        if (!el('device-rows')) { return; }

        loadFromURL();
        initFilters();
        initActions();
        initStream();
        initReachability();

        var initial = el('fleet-initial');
        var data = null;
        try {
            data = initial ? JSON.parse(initial.textContent) : null;
        } catch (e) {
            data = null;
        }
        if (data) {
            window.FleetStream.seed(data, null);
        } else {
            render();
        }
    });
})();
