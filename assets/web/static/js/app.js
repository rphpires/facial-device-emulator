/**
 * app.js - shell behavior: navigation rail, fleet meter, global actions.
 */
(function () {
    'use strict';

    var RAIL_KEY = 'fde.rail.expanded';

    function readPref(key) {
        try { return window.localStorage.getItem(key); } catch (e) { return null; }
    }

    function writePref(key, value) {
        try { window.localStorage.setItem(key, value); } catch (e) { /* private mode */ }
    }

    // ------------------------------------------------------------------
    // Rail
    // ------------------------------------------------------------------

    function initRail() {
        var shell = document.getElementById('shell');
        var toggle = document.getElementById('rail-toggle');
        if (!shell || !toggle) { return; }

        // Remembered across page loads.
        var expanded = readPref(RAIL_KEY) === 'true';
        apply(expanded);

        toggle.addEventListener('click', function () {
            expanded = !expanded;
            apply(expanded);
            writePref(RAIL_KEY, String(expanded));
        });

        function apply(open) {
            // An attribute, not inline styles: the transition stays in CSS
            // and prefers-reduced-motion keeps working.
            shell.setAttribute('data-rail', open ? 'expanded' : 'collapsed');
            toggle.setAttribute('aria-expanded', String(open));
            toggle.querySelector('.rail__label').textContent = open ? 'Collapse' : 'Expand';
        }
    }

    function markActiveNav() {
        var path = window.location.pathname;
        var items = document.querySelectorAll('.rail__item[data-nav]');

        for (var i = 0; i < items.length; i++) {
            if (items[i].getAttribute('data-nav') === path) {
                items[i].setAttribute('aria-current', 'page');
            }
        }
    }

    // ------------------------------------------------------------------
    // Fleet meter
    // ------------------------------------------------------------------

    var FleetMeter = (function () {
        var MAX_SEGMENTS = 60;

        function render(counts) {
            var bar = document.getElementById('meter-bar');
            var reading = document.getElementById('meter-reading');
            if (!bar || !counts) { return; }

            var states = []
                .concat(fill('running', counts.running))
                .concat(fill('stopped', counts.stopped))
                .concat(fill('disabled', counts.disabled));

            // Large fleets: one segment per emulator would be a blur. Above
            // the cap, segments become proportional.
            if (states.length > MAX_SEGMENTS) {
                states = sample(states, MAX_SEGMENTS);
            }

            bar.textContent = '';
            states.forEach(function (state) {
                var seg = document.createElement('span');
                seg.className = 'meter__seg';
                seg.setAttribute('data-state', state);
                bar.appendChild(seg);
            });

            bar.setAttribute('aria-label',
                counts.running + ' of ' + counts.total + ' emulators running, ' +
                counts.stopped + ' stopped, ' + counts.disabled + ' disabled');

            reading.textContent = '';
            var running = document.createElement('b');
            running.textContent = counts.running.toLocaleString('en-US');
            reading.appendChild(running);
            reading.appendChild(document.createTextNode(' / ' + counts.total.toLocaleString('en-US')));
        }

        function fill(state, count) {
            var out = [];
            for (var i = 0; i < count; i++) { out.push(state); }
            return out;
        }

        function sample(items, size) {
            var out = [];
            var step = items.length / size;
            for (var i = 0; i < size; i++) {
                out.push(items[Math.floor(i * step)]);
            }
            return out;
        }

        return { render: render };
    })();

    function initMeter() {
        var meter = document.getElementById('fleet-meter');
        var health = document.getElementById('meter-health');
        if (!meter) { return; }

        // Seed with what the server rendered, so the bar is not empty
        // before the snapshot arrives.
        FleetMeter.render({
            total: Number(meter.getAttribute('data-total')),
            running: Number(meter.getAttribute('data-running')),
            stopped: Number(meter.getAttribute('data-stopped')),
            disabled: Number(meter.getAttribute('data-disabled'))
        });

        window.FleetStream.subscribe('counts', FleetMeter.render);

        window.FleetStream.subscribe('status', function (state) {
            // Reconnecting keeps the numbers; it only warns they may be
            // stale.
            if (state === 'live') {
                health.textContent = '';
            } else if (state === 'reconnecting') {
                health.textContent = 'reconnecting…';
            } else {
                health.textContent = 'offline';
            }
        });
    }

    // ------------------------------------------------------------------
    // Global actions
    // ------------------------------------------------------------------

    function post(url) {
        return fetch(url, { method: 'POST' }).then(function (response) {
            if (!response.ok) { throw new Error('HTTP ' + response.status); }
            return response;
        });
    }

    function bindFleetAction(buttonID, url, doing, failure) {
        var button = document.getElementById(buttonID);
        if (!button) { return; }

        button.addEventListener('click', function () {
            button.disabled = true;
            post(url)
                .then(function () { window.Toast.ok(doing); })
                .catch(function () { window.Toast.err(failure); })
                .then(function () { button.disabled = false; });
        });
    }

    function initGlobalActions() {
        // Both run in the background on the server (202); progress shows
        // up on the meter and in the table through the stream.
        bindFleetAction('start-all', '/api/emulators/start',
            'Starting all emulators', 'Could not start the emulators');
        bindFleetAction('stop-all', '/api/emulators/stop',
            'Stopping all emulators', 'Could not stop the emulators');
    }

    document.addEventListener('DOMContentLoaded', function () {
        initRail();
        markActiveNav();
        initMeter();
        initGlobalActions();
        window.FleetStream.start();
    });

    window.FleetMeter = FleetMeter;
})();
