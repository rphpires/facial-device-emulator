/**
 * device-drawer.js — device detail: users stored on the emulator and the
 * settings written by the management system.
 *
 * Rows are built as DOM nodes with textContent rather than HTML template
 * strings, so no HTML escaping helper is needed.
 */
(function () {
    'use strict';

    var PER_PAGE = 10;
    var SEARCH_DEBOUNCE = 300;

    var state = {
        id: null,
        name: '',
        page: 1,
        total: 0,
        search: '',
        searchTimer: null
    };

    var previousFocus = null;
    var lastRequest = 0;

    function el(id) { return document.getElementById(id); }

    function drawerTitle(name, id) {
        return name + ' · ID ' + id;
    }

    // ------------------------------------------------------------------
    // Open / close
    // ------------------------------------------------------------------

    function open(id, name) {
        state.id = id;
        state.name = name || '';
        state.page = 1;
        state.search = '';
        state.total = 0;

        el('users-search').value = '';
        el('drawer-name').textContent = drawerTitle(state.name, id);

        var device = window.FleetStream.get(id);
        el('drawer-led').setAttribute('data-state', device ? device.status : 'stopped');

        previousFocus = document.activeElement;

        var drawer = el('device-drawer');
        drawer.hidden = false;
        el('drawer-scrim').hidden = false;
        // Next frame: the element must be visible for the transform
        // transition to run.
        window.requestAnimationFrame(function () {
            drawer.setAttribute('data-open', 'true');
        });

        selectTab('users');

        // Both tabs load on open, so switching tabs is instant.
        loadUsers();
        loadSettings();

        el('drawer-close').focus();
    }

    function close() {
        var drawer = el('device-drawer');
        drawer.setAttribute('data-open', 'false');
        el('drawer-scrim').hidden = true;

        window.setTimeout(function () { drawer.hidden = true; }, 200);

        if (previousFocus && previousFocus.focus) { previousFocus.focus(); }
        state.id = null;
    }

    function selectTab(tab) {
        var tabs = document.querySelectorAll('.drawer__tab');
        for (var i = 0; i < tabs.length; i++) {
            tabs[i].setAttribute('aria-selected',
                String(tabs[i].getAttribute('data-tab') === tab));
        }

        el('panel-users').hidden = tab !== 'users';
        el('panel-settings').hidden = tab !== 'settings';
    }

    // ------------------------------------------------------------------
    // Status messages inside a table
    // ------------------------------------------------------------------

    function tableMessage(tbody, columns, text, kind) {
        tbody.textContent = '';

        var tr = document.createElement('tr');
        var td = document.createElement('td');
        td.colSpan = columns;

        var box = document.createElement('div');
        box.className = kind === 'error' ? 'empty empty--error' : 'empty';

        var title = document.createElement('p');
        title.className = 'empty__title';
        title.textContent = text;
        box.appendChild(title);

        td.appendChild(box);
        tr.appendChild(td);
        tbody.appendChild(tr);
    }

    // ------------------------------------------------------------------
    // Users
    // ------------------------------------------------------------------

    // silent: reload triggered by the stream. Keeps the current list on
    // screen until the new one arrives instead of flashing "Loading…".
    function loadUsers(silent) {
        var tbody = el('users-body');
        if (!silent) { tableMessage(tbody, 5, 'Loading…'); }

        // Only the latest response is drawn: switching device, page or
        // search quickly must not leave an old query's list on screen.
        var request = ++lastRequest;

        var params = new URLSearchParams({
            page: String(state.page),
            per_page: String(PER_PAGE),
            search: state.search
        });

        fetch('/api/devices/' + state.id + '/users?' + params.toString())
            .then(function (response) {
                if (!response.ok) { throw new Error('HTTP ' + response.status); }
                return response.json();
            })
            .then(function (data) {
                if (request !== lastRequest) { return; }
                state.total = data.total || 0;
                renderUsers(data.users || []);
                renderPagination();
            })
            .catch(function (err) {
                if (request !== lastRequest) { return; }
                tableMessage(tbody, 5,
                    'Could not load users: ' + err.message, 'error');
                el('users-summary').textContent = '';
                el('users-prev').disabled = true;
                el('users-next').disabled = true;
            });
    }

    function renderUsers(users) {
        var tbody = el('users-body');

        if (users.length === 0) {
            tableMessage(tbody, 5, state.search
                ? 'No users match the search'
                : 'No users stored on this device');
            return;
        }

        tbody.textContent = '';

        users.forEach(function (user) {
            var tr = document.createElement('tr');

            tr.appendChild(cell(user.id, 'mono'));
            tr.appendChild(cell(user.name, ''));
            tr.appendChild(cell(user.card_no, ''));
            tr.appendChild(faceCell(user.has_face));
            tr.appendChild(cell(formatValidity(user.valid_to), 'num'));

            tbody.appendChild(tr);
        });
    }

    function cell(value, className) {
        var td = document.createElement('td');
        if (className) { td.className = className; }

        var text = value === null || value === undefined ? '' : String(value);
        if (text === '') {
            td.textContent = '—';
            td.style.color = 'var(--text-low)';
        } else {
            td.textContent = text;
        }

        return td;
    }

    function faceCell(hasFace) {
        var td = document.createElement('td');
        td.className = 'center';

        var badge = document.createElement('span');
        badge.className = 'badge';
        badge.setAttribute('data-state', hasFace ? 'running' : 'disabled');
        badge.textContent = hasFace ? 'Yes' : 'No';

        td.appendChild(badge);
        return td;
    }

    // ISO YYYY-MM-DD in UTC. Reading in UTC keeps the date from shifting a
    // day depending on the viewer's time zone.
    function formatValidity(value) {
        if (!value) { return ''; }

        var date = new Date(value);
        if (isNaN(date.getTime())) { return String(value); }

        var day = String(date.getUTCDate()).padStart(2, '0');
        var month = String(date.getUTCMonth() + 1).padStart(2, '0');
        return date.getUTCFullYear() + '-' + month + '-' + day;
    }

    function renderPagination() {
        var first = state.total === 0 ? 0 : (state.page - 1) * PER_PAGE + 1;
        var last = Math.min(state.page * PER_PAGE, state.total);

        el('users-summary').textContent = state.total === 0
            ? ''
            : first + '–' + last + ' of ' + state.total;

        el('users-prev').disabled = state.page <= 1;
        el('users-next').disabled = last >= state.total;
    }

    // ------------------------------------------------------------------
    // Settings
    // ------------------------------------------------------------------

    function loadSettings() {
        var tbody = el('settings-body');
        tableMessage(tbody, 2, 'Loading…');

        paintLog(window.FleetStream.get(state.id));

        fetch('/api/devices/' + state.id + '/settings')
            .then(function (response) {
                if (!response.ok) { throw new Error('HTTP ' + response.status); }
                return response.json();
            })
            .then(function (data) {
                renderSettings(data.settings || []);
            })
            .catch(function (err) {
                tableMessage(tbody, 2,
                    'Could not load settings: ' + err.message, 'error');
            });
    }

    function renderSettings(settings) {
        var tbody = el('settings-body');

        if (settings.length === 0) {
            tableMessage(tbody, 2, 'No settings stored');
            return;
        }

        tbody.textContent = '';

        settings.forEach(function (item) {
            var tr = document.createElement('tr');
            tr.appendChild(cell(item.cfg_id, ''));
            tr.appendChild(cell(item.value, 'mono'));
            tbody.appendChild(tr);
        });
    }

    var LOG_OFF_WARNING = 'Turning logging off deletes this emulator\'s log files. Continue?';

    // The flag can change in any emulator state. The links only show with
    // logging on, because turning it off deletes the files.
    function paintLog(device) {
        var log = el('drawer-log');
        var enabled = !!device && device.log_enabled === 1;
        log.checked = enabled;
        log.disabled = !device;
        el('drawer-log-links').hidden = !enabled;
        if (device) {
            el('drawer-log-view').href = '/api/devices/' + device.id + '/log';
            el('drawer-log-zip').href = '/api/devices/' + device.id + '/log.zip';
        }
    }

    // Saves immediately, like the table. The confirmation comes back through
    // the stream and repaints both the table and the drawer.
    function saveLog() {
        var log = el('drawer-log');
        var enabled = log.checked;
        if (!enabled && !window.confirm(LOG_OFF_WARNING)) {
            log.checked = true;
            return;
        }
        log.disabled = true;

        fetch('/api/devices/' + state.id + '/settings', {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ log_enabled: enabled })
        })
            .then(function (response) {
                if (!response.ok) { throw new Error('HTTP ' + response.status); }
                window.Toast.ok('Logging ' + (enabled ? 'enabled' : 'disabled'));
            })
            .catch(function () {
                log.checked = !enabled;
                window.Toast.err('Could not save the log setting');
            })
            .then(function () { paintLog(window.FleetStream.get(state.id)); });
    }

    // ------------------------------------------------------------------
    // Wiring
    // ------------------------------------------------------------------

    document.addEventListener('DOMContentLoaded', function () {
        if (!el('device-drawer')) { return; }

        el('drawer-close').addEventListener('click', close);
        el('drawer-scrim').addEventListener('click', close);
        el('drawer-log').addEventListener('change', saveLog);

        var tabs = document.querySelectorAll('.drawer__tab');
        for (var i = 0; i < tabs.length; i++) {
            tabs[i].addEventListener('click', function (event) {
                selectTab(event.currentTarget.getAttribute('data-tab'));
            });
        }

        // 300 ms debounce: typing should not fire one query per keystroke.
        el('users-search').addEventListener('input', function (event) {
            var value = event.target.value;
            window.clearTimeout(state.searchTimer);
            state.searchTimer = window.setTimeout(function () {
                state.search = value.trim();
                state.page = 1;
                loadUsers();
            }, SEARCH_DEBOUNCE);
        });

        el('users-prev').addEventListener('click', function () {
            if (state.page > 1) {
                state.page -= 1;
                loadUsers();
            }
        });

        el('users-next').addEventListener('click', function () {
            if (state.page * PER_PAGE < state.total) {
                state.page += 1;
                loadUsers();
            }
        });

        document.addEventListener('keydown', function (event) {
            if (event.key === 'Escape' && state.id !== null) { close(); }
        });

        // The drawer follows the open device: LED, log, name and — when the
        // management system writes or deletes users — the user list.
        window.FleetStream.subscribe('delta', function (data) {
            if (state.id === null) { return; }
            var id = Number(state.id);

            if (data.removed.indexOf(id) !== -1) {
                window.Toast.info('Device ' + id + ' was removed');
                close();
                return;
            }

            var device = null;
            data.devices.forEach(function (d) { if (d.id === id) { device = d; } });
            if (!device) { return; }

            var before = data.previous[id];
            el('drawer-led').setAttribute('data-state', device.status);
            el('drawer-name').textContent = drawerTitle(device.name, id);
            paintLog(device);

            if (before && before.total_users !== device.total_users) {
                loadUsers(true);
            }
        });
    });

    window.DeviceDrawer = { open: open, close: close };
})();
