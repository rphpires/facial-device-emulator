/**
 * FleetStream — the single connection to /events and the client-side fleet
 * state.
 *
 * Holds the whole fleet (id -> device) and keeps it current with what the
 * server sends. Pages read from here and subscribe to changes; nothing needs
 * a page reload to show a new, removed or state-changed device.
 *
 * Published events:
 *   'snapshot' -> { devices: [...], counts }            fleet replaced
 *   'delta'    -> { devices: [...], removed: [ids], counts, previous: {id: old} }
 *   'counts'   -> { total, running, stopped, disabled }
 *   'status'   -> 'live' | 'reconnecting' | 'down'      stream health
 */
(function () {
    'use strict';

    var INITIAL_BACKOFF = 1000;
    var MAX_BACKOFF = 30000;
    // Fallback polling kicks in only after this long without a stream.
    var FALLBACK_THRESHOLD = 30000;
    var FALLBACK_INTERVAL = 10000;

    var source = null;
    var backoff = INITIAL_BACKOFF;
    var reconnectTimer = null;
    var fallbackTimer = null;
    var droppedAt = null;
    var subscribers = { snapshot: [], delta: [], counts: [], status: [] };
    var fleet = new Map();

    var api = {
        state: 'down',
        counts: { total: 0, running: 0, stopped: 0, disabled: 0 },
        // True after the first full state (HTML or snapshot).
        ready: false,

        subscribe: function (event, fn) {
            if (!subscribers[event]) { return function () { }; }
            subscribers[event].push(fn);
            return function () {
                var i = subscribers[event].indexOf(fn);
                if (i !== -1) { subscribers[event].splice(i, 1); }
            };
        },

        get: function (id) {
            return fleet.get(Number(id)) || null;
        },

        all: function () {
            return Array.from(fleet.values());
        },

        /**
         * Seeds the state with the fleet the server embedded in the HTML.
         * The stream snapshot replaces it when it arrives.
         */
        seed: function (devices, counts) {
            if (api.ready) { return; }
            replace(devices || []);
            api.ready = true;
            publish('snapshot', { devices: api.all(), counts: counts || api.counts });
            // Without counts, the meter keeps what the server seeded in the
            // header's data-* attributes instead of being zeroed.
            if (counts) {
                api.counts = counts;
                publish('counts', api.counts);
            }
        },

        start: function () {
            if (source) { return; }
            connect();
        }
    };

    function publish(event, data) {
        subscribers[event].slice().forEach(function (fn) {
            try {
                fn(data);
            } catch (err) {
                console.error('FleetStream: "' + event + '" subscriber failed', err);
            }
        });
    }

    function setState(next) {
        if (api.state === next) { return; }
        api.state = next;
        publish('status', next);
    }

    function replace(devices) {
        fleet = new Map();
        devices.forEach(function (d) { fleet.set(d.id, d); });
    }

    function connect() {
        source = new EventSource('/events');

        source.addEventListener('open', function () {
            backoff = INITIAL_BACKOFF;
            droppedAt = null;
            stopFallback();
            setState('live');
        });

        source.addEventListener('snapshot', function (event) {
            var data = parse(event.data);
            if (!data) { return; }
            replace(data.devices || []);
            api.counts = data.counts;
            api.ready = true;
            setState('live');
            publish('snapshot', { devices: api.all(), counts: api.counts });
            publish('counts', api.counts);
        });

        source.addEventListener('delta', function (event) {
            var data = parse(event.data);
            if (!data) { return; }

            // previous lets subscribers compare before/after (e.g. the
            // drawer only reloads the user list when total_users changes).
            var previous = {};
            (data.devices || []).forEach(function (d) {
                previous[d.id] = fleet.get(d.id) || null;
                fleet.set(d.id, d);
            });
            (data.removed || []).forEach(function (id) {
                previous[id] = fleet.get(id) || null;
                fleet.delete(id);
            });

            api.counts = data.counts;
            publish('delta', {
                devices: data.devices || [],
                removed: data.removed || [],
                counts: data.counts,
                previous: previous
            });
            publish('counts', api.counts);
        });

        source.addEventListener('error', function () {
            // EventSource reconnects on its own while readyState is
            // CONNECTING. It only counts as a drop once it gives up.
            if (source.readyState !== EventSource.CLOSED) {
                setState('reconnecting');
                return;
            }

            source.close();
            source = null;

            if (droppedAt === null) { droppedAt = Date.now(); }
            setState('reconnecting');
            scheduleFallback();

            reconnectTimer = window.setTimeout(connect, backoff);
            backoff = Math.min(backoff * 2, MAX_BACKOFF);
        });
    }

    function parse(raw) {
        try {
            return JSON.parse(raw);
        } catch (err) {
            console.error('FleetStream: invalid payload', err, raw);
            return null;
        }
    }

    /**
     * Safety net: if the stream is not back within FALLBACK_THRESHOLD, poll
     * /api/status. Never overwrites the data with zeros, so a single network
     * failure cannot wipe the real counts.
     */
    function scheduleFallback() {
        if (fallbackTimer) { return; }

        fallbackTimer = window.setInterval(function () {
            if (droppedAt === null || Date.now() - droppedAt < FALLBACK_THRESHOLD) { return; }

            setState('down');

            fetch('/api/status')
                .then(function (r) { return r.ok ? r.json() : Promise.reject(r.status); })
                .then(function (data) {
                    api.counts = {
                        total: data.total_devices,
                        running: data.running_devices,
                        stopped: data.stopped_devices,
                        disabled: data.disabled_devices
                    };
                    publish('counts', api.counts);
                })
                .catch(function () { /* keep trying on the next tick */ });
        }, FALLBACK_INTERVAL);
    }

    function stopFallback() {
        if (fallbackTimer) {
            window.clearInterval(fallbackTimer);
            fallbackTimer = null;
        }
    }

    window.addEventListener('beforeunload', function () {
        if (reconnectTimer) { window.clearTimeout(reconnectTimer); }
        stopFallback();
        if (source) { source.close(); }
    });

    window.FleetStream = api;
})();
