import React, { useEffect, useRef, useState } from 'react';
import { createRoot } from 'react-dom/client';
import { Settings, Patterns, Phrases, Suggestions, Recordings } from './management.jsx';
import { GestureSymbols, gestureName } from './gesture-symbols.jsx';
import { t, setLanguage, languages } from './i18n.js';
import './style.css';

const pages = { live: 'Live', patterns: 'Patterns', recordings: 'Recordings', settings: 'Profiles', phrases: 'Phrases', suggestions: 'Suggestions' };
const directions = {
  NW: [-650, 650], N: [0, 900], NE: [650, 650],
  W: [-900, 0], C: [0, 0], E: [900, 0],
  SW: [-650, -650], S: [0, -900], SE: [650, -650],
};

async function request(token, path, method = 'GET', body) {
  const headers = token ? { Authorization: 'Bearer ' + token } : {};
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  const response = await fetch('/v1' + path, { method, headers, body: JSON.stringify(body) });
  if (!response.ok) {
    const error = await response.json().catch(() => ({}));
    throw new Error(error.error || response.statusText);
  }
  return [202, 204].includes(response.status) ? null : response.json();
}

function speak(message, token) {
  const browserVoice = () => {
    if (!window.speechSynthesis) return;
    const utterance = new SpeechSynthesisUtterance(message.text);
    utterance.lang = message.language || 'en';
    // A voice of the message's language, so Romanian is not read in English.
    const voice = speechSynthesis.getVoices().find((v) => v.lang.toLowerCase().startsWith(utterance.lang));
    if (voice) utterance.voice = voice;
    window.speechSynthesis.speak(utterance);
  };
  if (!message.audio_url) {
    browserVoice();
    return;
  }
  const path = new URL(message.audio_url, location.href).pathname;
  const headers = token ? { Authorization: 'Bearer ' + token } : {};
  fetch(path, { headers }).then(async (response) => {
    if (!response.ok) throw new Error('Audio unavailable');
    const url = URL.createObjectURL(await response.blob());
    const audio = new Audio(url);
    audio.onended = () => URL.revokeObjectURL(url);
    try { await audio.play(); } catch (error) { URL.revokeObjectURL(url); throw error; }
  }).catch(browserVoice);
}

const controlSelector = 'a[href], summary, button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled])';

function moveFocus(key) {
  const controls = [...document.querySelectorAll(controlSelector)].filter((el) => el.getClientRects().length);
  const index = controls.indexOf(document.activeElement);
  if (key === 'key.enter' || key === 'scan.select') {
    if (index >= 0 && controls[index].matches('a,button,summary')) controls[index].click();
    return;
  }
  const offset = ['key.left', 'key.up', 'scan.prev'].includes(key) ? -1 : 1;
  const target = index < 0 ? controls[0] : controls[(index + offset + controls.length) % controls.length];
  target?.focus();
}

function App() {
  const [page, setPage] = useState(() => Object.hasOwn(pages, location.hash.slice(1)) ? location.hash.slice(1) : 'live');
  const [token, setToken] = useState(() => sessionStorage.getItem('actspeed.token') || '');
  const [tokenInput, setTokenInput] = useState(token);
  const [profiles, setProfiles] = useState([]);
  const [devices, setDevices] = useState([]);
  const [profileId, setProfileId] = useState(() => localStorage.getItem('actspeed.profile') || '');
  const [moving, setMoving] = useState({}); // unassigned joystick id -> last movement time
  const [testing, setTesting] = useState(false);
  const [state, setState] = useState(null);
  const [events, setEvents] = useState([]);
  const [recording, setRecording] = useState(null);
  const trace = useRef([]);
  const testSequence = useRef(0);
  const [sampleCount, setSampleCount] = useState(0);
  const [connection, setConnection] = useState('Connecting');
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [cursor, setCursor] = useState(null);
  const cursorRef = useRef(null);
  const selected = useRef(profileId);
  selected.current = profileId;
  const lastAlert = useRef('');
  const api = (path, method, body) => request(token, path, method, body);
  const profilePath = '/profiles/' + encodeURIComponent(profileId);
  const chooseProfile = (id) => {
    localStorage.setItem('actspeed.profile', id);
    setProfileId(id);
  };

  const run = async (action) => {
    setError('');
    try { return await action(); }
    catch (error) { setError(error.message); }
  };
  const refreshDevices = async () => {
    const [people, list] = await Promise.all([api('/profiles'), api('/devices')]);
    setProfiles(people || []);
    setDevices(list || []);
    setProfileId((current) => people?.some((p) => p.id === current) ? current : people?.[0]?.id || '');
  };
  const refreshState = async (id = selected.current) => {
    if (!id) return;
    const next = await api('/profiles/' + encodeURIComponent(id) + '/state');
    if (id === selected.current) setState({ ...next, scheme_state: next.scheme });
  };
  useEffect(() => {
    const change = () => setPage(Object.hasOwn(pages, location.hash.slice(1)) ? location.hash.slice(1) : 'live');
    window.addEventListener('hashchange', change);
    return () => window.removeEventListener('hashchange', change);
  }, []);
  useEffect(() => {
    run(refreshDevices);
    const timer = setInterval(() => refreshDevices().catch(() => {}), 5000);
    return () => clearInterval(timer);
  }, [token]);
  useEffect(() => {
    setState(null);
    setEvents([]);
    setCursor(null);
    cursorRef.current = null;
    if (profileId) run(() => refreshState(profileId));
  }, [token, profileId]);

  useEffect(() => {
    let socket, retry, stopped;
    const open = () => {
      const url = new URL('/v1/ws', location.href);
      url.protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
      if (token) url.searchParams.set('token', token);
      socket = new WebSocket(url);
      socket.onopen = () => { if (!stopped) setConnection('Connected'); };
      socket.onclose = () => {
        if (stopped) return;
        setConnection('Disconnected. Retrying…');
        retry = setTimeout(open, 2000);
      };
      socket.onmessage = ({ data }) => {
        if (stopped) return;
        let event;
        try { event = JSON.parse(data); } catch { return; }
        if (event.type === 'alert' && event.seq !== lastAlert.current) {
          lastAlert.current = event.seq;
          setNotice(event.data.message);
        }
        if (event.type === 'unassigned_input') {
          setMoving((current) => ({ ...current, [event.data.device_id]: Date.now() }));
          refreshDevices().catch(() => {});
          return;
        }
        if (event.profile !== selected.current) return;
        const { type, data: payload } = event;
        if (type === 'sample') {
          if (trace.current.length < 12000) {
            trace.current.push({ ...payload, received: event.ts });
            if (trace.current.length === 1 || trace.current.length % 15 === 0) setSampleCount(trace.current.length);
          }
          return;
        }
        if (type === 'speak') speak(payload, token);
        if (type === 'action') {
          if (payload.key === 'pointer.move') {
            const last = cursorRef.current || { x: innerWidth / 2, y: innerHeight / 2 };
            const point = {
              x: Math.max(0, Math.min(innerWidth - 1, last.x + payload.params.dx)),
              y: Math.max(0, Math.min(innerHeight - 1, last.y + payload.params.dy)),
            };
            cursorRef.current = point;
            setCursor(point);
            return;
          }
          if (payload.key === 'pointer.click' && cursorRef.current) {
            const { x, y } = cursorRef.current;
            const target = document.elementFromPoint(x, y)?.closest(controlSelector);
            target?.focus();
            if (target?.matches('button:not([disabled]),a,summary')) target.click();
          }
          if (['key.up', 'key.down', 'key.left', 'key.right', 'key.enter', 'scan.prev', 'scan.next', 'scan.select'].includes(payload.key)) {
            moveFocus(payload.key);
          }
        }
        if (type === 'text_update') {
          setState((current) => current && { ...current, buffer: payload.buffer, predictions: payload.predictions, scheme_state: payload.scheme_state, suggest: payload.suggest ?? current.suggest });
        }
        if (['mode_changed', 'suggestion', 'suggestion_resolved', 'suggestion_applied', 'devices_changed'].includes(type)) {
          refreshState(event.profile).catch((err) => setError(err.message));
        }
        if (type === 'devices_changed' || type === 'profile_changed') refreshDevices().catch(() => {});
        if (type === 'profile_changed') refreshState(event.profile).catch(() => {});
        if (type !== 'text_update') setEvents((items) => [event, ...items].slice(0, 30));
      };
    };
    open();
    return () => { stopped = true; clearTimeout(retry); socket?.close(); };
  }, [token]);

  const setMode = (change) => run(async () => { await api(profilePath + '/mode', 'POST', change); await refreshState(); });
  const resolve = (id, action) => run(async () => {
    await api('/suggestions/' + id + '/' + action, 'POST');
    await refreshState();
  });
  const record = () => run(async () => {
    if (recording) {
      await api('/recordings/' + recording.id + '/stop', 'POST');
      setSampleCount(trace.current.length);
      setRecording(null);
      setNotice(t('Recording saved.'));
    } else {
      trace.current = [];
      setSampleCount(0);
      setRecording(await api(profilePath + '/recordings', 'POST', { label: t('Joystick practice') }));
      setNotice(t('Recording started.'));
    }
  });
  const downloadTrace = () => {
    const csv = ['seq,device_ms,received_ms,x,y', ...trace.current.map(({ seq, t, received, x, y }) => [seq, t, received, x, y].join(','))].join('\n');
    const url = URL.createObjectURL(new Blob([csv], { type: 'text/csv' }));
    const link = document.createElement('a');
    link.href = url;
    link.download = 'joystick-trace.csv';
    link.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  };

  setLanguage(profiles.find((p) => p.id === profileId)?.language);
  return <>
    <a className="skip" href="#main-content" onClick={(e) => { e.preventDefault(); document.querySelector('main').focus(); }}>{t('Skip to content')}</a>
    <header className="masthead">
      <a className="brand" href="#live">ActSpeed</a>
      <span className="connection" role="status">{t(connection)}</span>
    </header>
    <div className="layout">
      <aside>
        <nav aria-label="Dashboard">{Object.entries(pages).map(([key, label]) =>
          <a key={key} href={'#' + key} aria-current={page === key ? 'page' : undefined}>{t(label)}</a>
        )}</nav>
        <details>
          <summary>{t('Server access')}</summary>
          <form onSubmit={(e) => {
            e.preventDefault();
            sessionStorage.setItem('actspeed.token', tokenInput.trim());
            setToken(tokenInput.trim());
          }}>
            <label>{t('Admin token')}<input type="password" value={tokenInput} onChange={(e) => setTokenInput(e.target.value)} /></label>
            <button type="submit">{t('Save token')}</button>
          </form>
        </details>
      </aside>
      <main id="main-content" tabIndex={-1}>
        {error && <p className="message error" role="alert">{error}</p>}
        {notice && <p className="message" role="status">{notice} <button onClick={() => setNotice('')}>{t('Dismiss')}</button></p>}
        {page === 'live' && <>
          <div className="page-title">
            <h1>{t('Live')}</h1>
            <div className="actions">
              <label>{t('Profile')}<select value={profileId} disabled={Boolean(recording)} onChange={(e) => chooseProfile(e.target.value)}>
                {profiles.length === 0 && <option value="">{t('No profiles yet')}</option>}
                {profiles.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
              </select></label>
              {profileId && <label>{t('Language')}<select value={profiles.find((p) => p.id === profileId)?.language || 'en'}
                onChange={(e) => run(async () => { await api(profilePath, 'PUT', { language: e.target.value }); await refreshDevices(); })}>
                {Object.entries(languages).map(([code, name]) => <option key={code} value={code}>{name}</option>)}
              </select></label>}
              <a className="button" href="#settings">{t('Manage devices')}</a>
            </div>
          </div>
          {profileId && <DeviceStrip devices={devices.filter((d) => d.profile_id === profileId)} primary={state?.primary_client} />}
          {devices.some((d) => !d.profile_id) && <p className="message">
            {t('{n} new device(s) waiting to be assigned.', { n: devices.filter((d) => !d.profile_id).length })} <a href="#settings">{t('Assign them on Profiles')}</a>.
          </p>}
          <section className="panel">
            <div className="panel-head"><h2>{t('Communication')}</h2>
              {state && <span className="tag">{t(state.paused ? 'Paused' : state.mode === 'text' ? 'Text' : 'Navigation')}</span>}
            </div>
            {state?.field && <p className="field-note">{t('Typing into {field}: the message is typed there, not spoken.', { field: state.field.hint || state.field.app || t('a text field') })}</p>}
            <p className="composition" role="status" aria-atomic="true">{state?.buffer || t('No message composed')}<span className="cursor" aria-hidden="true">▌</span></p>
            {state?.mode === 'text' && state?.suggest ? <WritingHelp state={state} />
              : state?.predictions?.length > 0 && <p>{t('Predictions')} · {state.predictions.join(' · ')}</p>}
            {state?.scheme_state?.pending && <p>{t('Morse input')} · <code>{state.scheme_state.pending}</code></p>}
            {state?.scheme_state?.cells && <table className="keyboard" aria-label={t('Joystick keyboard')}>
              <tbody>{state.scheme_state.cells.map((row, r) => <tr key={r}>
                {row.map((cell, c) => <td key={c} aria-current={r === state.scheme_state.row && c === state.scheme_state.col ? 'true' : undefined}>{cell.label || '—'}</td>)}
              </tr>)}</tbody>
            </table>}
            {state && <div className="actions">
              <button onClick={() => setMode({ mode: state.mode === 'nav' ? 'text' : 'nav' })}>{t(state.mode === 'nav' ? 'Switch to text' : 'Switch to navigation')}</button>
              <button onClick={() => setMode({ paused: !state.paused })}>{t(state.paused ? 'Resume' : 'Pause')}</button>
            </div>}
            {state?.pending_suggestion && <div className="suggestion">
              <h3>{t(state.pending_suggestion.kind === 'correct' ? 'Suggested correction' : 'Suggested gesture match')}</h3>
              <p>{state.pending_suggestion.candidates?.[0]?.value}</p>
              <p>{t('Flick')} <GestureSymbols sequence="E" /> {t('to accept or')} <GestureSymbols sequence="W" /> {t('to reject.')}</p>
              <div className="actions"><button onClick={() => resolve(state.pending_suggestion.id, 'accept')}>{t('Accept')}</button>
                <button onClick={() => resolve(state.pending_suggestion.id, 'reject')}>{t('Reject')}</button></div>
            </div>}
          </section>
          <div className="columns">
            <section className="panel">
              <h2>{t('Gesture practice')}</h2>
              <p><GestureSymbols sequence="CW" /> {t('switches modes.')} <GestureSymbols sequence="CCW" /> {t('activates the current navigation target or selects the highlighted grid cell.')}</p>
              <p>{t('In Corner alphabet mode, flick')} <GestureSymbols sequence="S" /> {t('to speak as typed. Use')} <GestureSymbols sequence="CCW" /> {t('to request a correction.')}</p>
              <p>{t('For one letter, trace the corners continuously or start each flick within {ms} ms of the last release. Pause at center between letters.', { ms: state?.corner_gap_ms ?? 300 })}</p>
              <div className="actions">
                <button disabled={!state} onClick={record}>{t(recording ? 'Stop recording' : 'Record gesture trace')}</button>
                <button disabled={!sampleCount} onClick={downloadTrace}>{t('Download trace CSV')}</button>
              </div>
              {recording && <p>{t('Recording · {n} samples', { n: sampleCount })}</p>}
              <label className="check"><input type="checkbox" checked={testing} disabled={!profileId} onChange={(e) => setTesting(e.target.checked)} /> {t('Show the on-screen test joystick')}</label>
              {testing && profileId && <TestJoystick send={(x, y) => api(profilePath + '/sample', 'POST', { seq: ++testSequence.current, t: Math.round(performance.now()), x, y })}
                done={() => run(() => refreshState())} onError={setError} />}
            </section>
            <section className="panel">
              <h2>{t('Activity')}</h2>
              {events.length ? <ul className="activity">{events.map((event) => <li key={event.seq}>
                <time>{new Date(event.ts).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })}</time>
                <span>{eventLabel(event)}</span>
              </li>)}</ul> : <p>{t('No activity yet.')}</p>}
            </section>
          </div>
        </>}
        {page === 'settings' && <Settings api={api} run={run} devices={devices} profiles={profiles} reload={refreshDevices}
          profileId={profileId} chooseProfile={chooseProfile} moving={moving} key={token} />}
        {page === 'patterns' && <Patterns api={api} run={run} key={token} />}
        {page === 'phrases' && <Phrases api={api} run={run} language={profiles.find((p) => p.id === profileId)?.language || 'en'} key={token} />}
        {page === 'suggestions' && <Suggestions api={api} run={run} key={token} />}
        {page === 'recordings' && <Recordings api={api} run={run} key={token} />}
      </main>
    </div>
    {cursor && <div className="cursor" aria-hidden="true" style={{ left: cursor.x, top: cursor.y }} />}
  </>;
}

// The selected person's devices: joysticks give input, phones show the panel.
// Only the primary phone taps, speaks and reads the screen.
function DeviceStrip({ devices, primary }) {
  const joysticks = devices.filter((d) => d.kind === 'joystick');
  const clients = devices.filter((d) => d.kind === 'client');
  const dot = (d) => <span className={'dot ' + (d.status === 'online' ? 'on' : 'off')} aria-label={t(d.status === 'online' ? 'online' : 'offline')} />;
  const none = <p>{t('None yet.')} <a href="#settings">{t('Add one')}</a>.</p>;
  return <section className="panel device-strip" aria-label={t('Devices of this profile')}>
    <div><h2>{t('Joysticks')}</h2>
      {joysticks.length ? <ul>{joysticks.map((d) => <li key={d.id}>{dot(d)}{d.name || d.id}</li>)}</ul> : none}
    </div>
    <div><h2>{t('Android')}</h2>
      {clients.length ? <ul>{clients.map((d) => <li key={d.id}>{dot(d)}{d.name || d.id}{d.id === primary && <span className="tag">{t('acts')}</span>}</li>)}</ul> : none}
    </div>
  </section>;
}

function eventLabel({ type, data }) {
  if (type === 'gesture') return <>{t('Gesture')} <GestureSymbols sequence={data.sequence} />
    {data.raw_sequence && data.sequence !== data.raw_sequence && <> · {t('raw')} <GestureSymbols sequence={data.raw_sequence} /></>}
  </>;
  if (type === 'speak') return t('Spoken') + ' · ' + data.text;
  if (type === 'assist') return t('Screen read') + ' · ' + data.summary;
  if (type === 'memory') return t('Remembered') + ' · ' + data.content;
  if (type === 'action') return t('Action') + ' · ' + (data.params?.label || t(data.label || data.key));
  if (type === 'unmatched') return <>{t('Unmatched')} · <GestureSymbols sequence={data.sequence} /></>;
  return t(type.replaceAll('_', ' ')) + (data.context ? ' · ' + data.context : '');
}

const wait = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

function TestJoystick({ send, done, onError }) {
  const [busy, setBusy] = useState(false);
  const play = async (token) => {
    setBusy(true);
    try {
      if (token in directions) {
        await send(...directions[token]);
        await wait(100);
      } else if (token === 'NW NE') {
        for (const dir of ['NW', 'N', 'NE']) {
          await send(...directions[dir]);
          await wait(85);
        }
      } else {
        for (let i = 0; i <= 16; i++) {
          const angle = (token === 'CW' ? -1 : 1) * i * Math.PI / 8;
          await send(Math.round(850 * Math.cos(angle)), Math.round(850 * Math.sin(angle)));
          await wait(25);
        }
      }
    } catch (error) { onError(error.message); }
    finally {
      try { await send(0, 0); await done(); }
      catch (error) { onError(error.message); }
      setBusy(false);
    }
  };
  return <fieldset disabled={busy}>
    <legend>{t('On-screen test joystick')}</legend>
    <div className="test-pad">
      {['NW', 'N', 'NE', 'W', 'C', 'E', 'SW', 'S', 'SE'].map((dir) =>
        <button key={dir} onClick={() => play(dir)} aria-label={gestureName(dir)} title={gestureName(dir)}>
          <GestureSymbols sequence={dir} />
        </button>)}
    </div>
    <div className="actions">
      <button onClick={() => play('CW')} aria-label={gestureName('CW')} title={gestureName('CW')}><GestureSymbols sequence="CW" /></button>
      <button onClick={() => play('CCW')} aria-label={gestureName('CCW')} title={gestureName('CCW')}><GestureSymbols sequence="CCW" /></button>
      <button onClick={() => play('NW NE')} aria-label={t('Continuous up-left to up-right')}>{t('Continuous')} <GestureSymbols sequence="NW NE" /></button>
    </div>
  </fieldset>;
}

createRoot(document.getElementById('root')).render(<App />);

// Writing help, at three levels, each item after the gesture that picks it.
// The first of each list is the most likely; the lists keep their order.
function WritingHelp({ state }) {
  const { letters = [], words = [], phrases = [] } = state.suggest;
  const nextWord = !state.buffer || state.buffer.endsWith(' ');
  const corners = state.context === 'text.corners';
  return <div className="writing-help">
    {letters.length > 0 && <Choices label={t('Next letter')} items={letters} />}
    {words.length > 0 && <Choices label={t(nextWord ? 'Next word' : 'Words')} items={words} />}
    {phrases.length > 0 && <Choices label={t('Phrases')} items={phrases} column />}
    {corners && <p className="controls">
      <GestureSymbols sequence="E" /> {t('space')} · <GestureSymbols sequence="W" /> {t('delete')} · <GestureSymbols sequence="S" /> {t(state.field ? 'type it' : 'speak')} · <GestureSymbols sequence="CCW" /> {t('fix, then speak')}
    </p>}
  </div>;
}

function Choices({ label, items, column }) {
  return <div className="choices">
    <h3>{label}</h3>
    <ul className={column ? 'column' : 'row'}>
      {items.map((c, i) => <li key={c.text} className={i === 0 ? 'first' : undefined}>
        {c.sequence && <kbd><GestureSymbols sequence={c.sequence} /></kbd>}
        <span className={c.sequence ? undefined : 'muted'}>{c.text}</span>
        {c.source === 'ai' && <span className="ai" title={t('Suggested by Claude')} aria-label={t('suggested by Claude')}> ✦</span>}
      </li>)}
    </ul>
  </div>;
}
