// Emulator form: create one, create a range, and edit.
// One modal, with the fields that differ swapped per mode; the alternative
// was three forms repeating the same six fields.

(function () {
  const modal = document.getElementById('emulator-form-modal');
  if (!modal) return;

  const el = (id) => document.getElementById(id);
  const title = el('emulator-form-title');
  const errorBox = el('emulator-form-error');

  let mode = 'create'; // 'create' | 'range' | 'edit'
  let editingID = null;

  function showFields() {
    const range = mode === 'range';
    el('field-name').hidden = range;
    el('field-prefix').hidden = !range;
    el('field-port').hidden = range;
    el('field-port-range').hidden = !range;
    // Editing requires a stopped emulator, so "start after creating" does
    // not apply.
    el('field-autostart').hidden = mode === 'edit';
  }

  function clearError() {
    errorBox.hidden = true;
    errorBox.textContent = '';
  }

  function showError(text) {
    errorBox.textContent = text;
    errorBox.hidden = false;
  }

  // Blank-form values. Used both to open "New emulator" / "Create range"
  // clean and as the base for editing, which then overwrites only the
  // fields the device actually has.
  function resetFields() {
    el('emulator-name').value = '';
    el('emulator-prefix').value = '';
    el('emulator-model').value = 'Hikvision';
    el('emulator-ip').value = '127.0.0.1';
    el('emulator-port').value = 4000;
    el('emulator-port-start').value = 4000;
    el('emulator-port-end').value = 4009;
    el('emulator-interval').value = 10;
    el('emulator-enabled').checked = true;
    el('emulator-autostart').checked = false;
  }

  function open(data) {
    clearError();
    // Always reset first, otherwise opening "New emulator" after an edit
    // would reuse the previous name, port and IP left in the DOM.
    resetFields();

    if (data && data.id) {
      mode = 'edit';
      editingID = data.id;
      title.textContent = `Edit emulator ${data.id}`;
      el('emulator-name').value = data.name || '';
      el('emulator-model').value = data.model || 'Hikvision';
      el('emulator-port').value = data.port || 4000;
      // PUT is a full replacement: without these three, the reset values
      // above would silently overwrite the device's real IP, interval and
      // enabled flag.
      el('emulator-ip').value = data.ip_address || '127.0.0.1';
      el('emulator-interval').value =
        Number.isFinite(data.event_interval) ? data.event_interval : 10;
      el('emulator-enabled').checked = !!data.enabled;
    } else if (data && data.range) {
      mode = 'range';
      editingID = null;
      title.textContent = 'Create a range of emulators';
    } else {
      mode = 'create';
      editingID = null;
      title.textContent = 'New emulator';
    }

    showFields();
    modal.showModal();
  }

  window.openEmulatorForm = open;

  el('new-emulator')?.addEventListener('click', () => open(null));
  el('new-emulator-range')?.addEventListener('click', () => open({ range: true }));
  el('emulator-form-cancel').addEventListener('click', () => modal.close());

  function commonBody() {
    return {
      model: el('emulator-model').value,
      ip_address: el('emulator-ip').value.trim(),
      event_interval: Number(el('emulator-interval').value),
      enabled: el('emulator-enabled').checked,
      auto_start: el('emulator-autostart').checked,
    };
  }

  function buildRequest() {
    if (mode === 'range') {
      return {
        url: '/api/emulators/range',
        method: 'POST',
        body: {
          ...commonBody(),
          name_prefix: el('emulator-prefix').value.trim(),
          port_start: Number(el('emulator-port-start').value),
          port_end: Number(el('emulator-port-end').value),
        },
      };
    }

    const body = {
      ...commonBody(),
      name: el('emulator-name').value.trim(),
      port: Number(el('emulator-port').value),
    };

    if (mode === 'edit') {
      delete body.auto_start;
      return { url: `/api/emulators/${editingID}`, method: 'PUT', body };
    }
    return { url: '/api/emulators', method: 'POST', body };
  }

  function reportResult(body) {
    const ids = [];
    if (body.emulator) ids.push(body.emulator.id);
    (body.created || []).forEach((c) => ids.push(c.id));
    window.DevicesTable?.highlight(ids);

    if (mode === 'edit') {
      window.Toast.ok(`Emulator ${editingID} updated`);
    } else if (ids.length > 1) {
      window.Toast.ok(`${ids.length} emulators created`);
    } else {
      window.Toast.ok('Emulator created');
    }

    const failures = body.start_errors || (body.start_error ? [{ error: body.start_error }] : []);
    if (failures.length > 0) {
      const which = failures.length === 1 ? 'one emulator' : `${failures.length} emulators`;
      window.Toast.err(`Created, but ${which} failed to start: ${failures[0].error}`);
    }
  }

  el('emulator-form-save').addEventListener('click', async () => {
    clearError();
    const { url, method, body } = buildRequest();
    const save = el('emulator-form-save');
    save.disabled = true;

    try {
      const resp = await fetch(url, {
        method,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      });
      const data = await resp.json();

      if (!resp.ok) {
        // A port conflict carries the raw list; repeating it tells the
        // operator which port to change.
        let msg = data.error || 'Could not save';
        if (Array.isArray(data.conflicts) && data.conflicts.length > 0) {
          msg = `Ports already in use: ${data.conflicts.join(', ')}`;
        }
        showError(msg);
        save.disabled = false;
        return;
      }

      modal.close();
      save.disabled = false;
      // The new rows arrive on the stream; no page reload.
      reportResult(data);
    } catch (e) {
      showError(`Network error: ${e.message}`);
      save.disabled = false;
    }
  });
})();
