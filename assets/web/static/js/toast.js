/**
 * Toast — non-blocking notifications.
 *
 * Used instead of alert(): alert() blocks the thread, needs a click to
 * dismiss and does not stack, so starting eight emulators would queue
 * eight dialogs.
 */
(function () {
    'use strict';

    var DURATION = { ok: 3000, info: 4000, err: 6000 };

    function container() {
        var el = document.getElementById('toast-region');
        if (!el) {
            el = document.createElement('div');
            el.id = 'toast-region';
            el.className = 'toast-region';
            // polite, not assertive: these confirm actions, not emergencies.
            el.setAttribute('role', 'status');
            el.setAttribute('aria-live', 'polite');
            document.body.appendChild(el);
        }
        return el;
    }

    function symbol(kind) {
        if (kind === 'err') { return 'alert'; }
        if (kind === 'ok') { return 'check'; }
        return 'info';
    }

    function show(kind, message) {
        var toast = document.createElement('div');
        toast.className = 'toast toast--' + kind;

        var icon = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
        icon.setAttribute('class', 'icon');
        var use = document.createElementNS('http://www.w3.org/2000/svg', 'use');
        use.setAttribute('href', '/static/icons.svg#' + symbol(kind));
        icon.appendChild(use);

        var text = document.createElement('span');
        // textContent, not innerHTML: the message may carry error text from
        // the server.
        text.textContent = message;

        toast.appendChild(icon);
        toast.appendChild(text);
        container().appendChild(toast);

        window.setTimeout(function () {
            toast.classList.add('toast--leaving');
            window.setTimeout(function () { toast.remove(); }, 200);
        }, DURATION[kind] || 4000);
    }

    window.Toast = {
        ok: function (msg) { show('ok', msg); },
        err: function (msg) { show('err', msg); },
        info: function (msg) { show('info', msg); }
    };
})();
