import React, { useEffect, useState } from 'react';
import { GestureSymbols, gestureCodes, gestureGlyphs, gestureName } from './gesture-symbols.jsx';
import { t, languages } from './i18n.js';

const modes = ['any', 'nav', 'nav.pointer', 'nav.gesture', 'text', 'text.corners', 'text.twodir', 'text.grid'];
const timings = [
  ['dead_zone', 'Activation radius', 400], ['release', 'Release radius', 250],
  ['direction_ms', 'Direction dwell (ms)', 35], ['neutral_ms', 'Neutral settle (ms)', 40],
  ['hold_ms', 'Hold threshold (ms)', 600], ['center_x', 'Center X', 0], ['center_y', 'Center Y', 0],
];

function useList(path, api, run) {
  const [items, setItems] = useState([]);
  const reload = async () => setItems(await api(path) || []);
  useEffect(() => { run(reload); }, [path]);
  return [items, reload];
}

export function Settings({ api, run, devices, profiles, reload, profileId, chooseProfile, moving }) {
  const [creating, setCreating] = useState(false);
  const profile = creating ? undefined : profiles.find((p) => p.id === profileId);
  const settings = profile?.settings || {};
  const signal = settings.signal || {};
  const save = (event) => {
    event.preventDefault();
    const fields = new FormData(event.currentTarget);
    const number = (key) => Number(fields.get(key));
    run(async () => {
      const updated = await api('/profiles' + (profile ? '/' + profile.id : ''), profile ? 'PUT' : 'POST', {
        name: fields.get('name'), language: fields.get('language'), input_method: profile?.input_method || 'mouth',
        ai_enabled: fields.has('ai'), settings: {
          ...settings, nav_style: fields.get('nav'), text_scheme: fields.get('scheme'),
          gap_ms: number('gap_ms'), corner_gap_ms: number('corner_gap_ms'), dwell_select_ms: number('dwell_select_ms'),
          signal: {
            ...signal, ...Object.fromEntries(timings.map(([key]) => [key, number(key)])),
            invert_x: fields.has('invert_x'), invert_y: fields.has('invert_y'), diagonals: fields.has('diagonals'),
          },
        },
      });
      await reload();
      setCreating(false);
      chooseProfile(updated.id);
    });
  };
  return <>
    <div className="page-title">
      <h1>{t('Profiles')}</h1>
      <div className="actions">
        <label>{t('Profile')}<select value={creating ? '' : profileId} onChange={(e) => {
          setCreating(e.target.value === '');
          if (e.target.value) chooseProfile(e.target.value);
        }}>
          <option value="">{t('New profile')}</option>
          {profiles.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
        </select></label>
      </div>
    </div>
    <div className="columns">
      {profile ? <ProfileDevices api={api} run={run} devices={devices} profile={profile} reload={reload} moving={moving} />
        : <section className="panel"><h2>{t('Devices')}</h2><p>{t('Save the profile first, then add its joysticks and Android phones.')}</p></section>}
      <section className="panel">
        <h2>{t(profile ? 'Joystick settings' : 'New profile')}</h2>
        <form key={profile?.id || 'new'} onSubmit={save}>
          <div className="fields">
            <label>{t('Name')}<input name="name" required defaultValue={profile?.name} /></label>
            <label>{t('Language')}<select name="language" defaultValue={profile?.language || 'en'}>
              {Object.entries(languages).map(([code, label]) => <option key={code} value={code}>{label}</option>)}
            </select></label>
            <label>{t('Navigation style')}<select name="nav" defaultValue={settings.nav_style || 'gesture'}>
              <option value="gesture">{t('Gesture navigation')}</option><option value="pointer">{t('Analog pointer')}</option>
            </select></label>
            <label>{t('Text input')}<select name="scheme" defaultValue={settings.text_scheme || 'corners'}>
              <option value="corners">{t('Corner alphabet')}</option><option value="twodir">{t('Two-direction Morse')}</option><option value="grid">{t('Letter grid')}</option>
            </select></label>
            {timings.map(([key, label, fallback]) => <label key={key}>{t(label)}
              <input name={key} type="number" required defaultValue={signal[key] ?? fallback}
                min={key.startsWith('center') ? -1000 : 1} max={key.endsWith('_ms') ? 5000 : 1000} />
            </label>)}
            <label>{t('Compound gesture gap (ms)')}<input name="gap_ms" type="number" required min="50" defaultValue={settings.gap_ms || 500} /></label>
            <label>{t('Corner letter gap (ms)')}<input name="corner_gap_ms" type="number" required min="50" defaultValue={settings.corner_gap_ms || 300} /></label>
            <label>{t('Grid dwell selection (ms)')}<input name="dwell_select_ms" type="number" required min="0" defaultValue={settings.dwell_select_ms || 0} /></label>
          </div>
          <p className="hint">{t('Language')}: {t('What the person writes and hears, and the language of this dashboard and the phone while this profile is selected.')}</p>
          <div className="actions">
            <label className="check"><input name="invert_x" type="checkbox" defaultChecked={signal.invert_x} /> {t('Invert X')}</label>
            <label className="check"><input name="invert_y" type="checkbox" defaultChecked={signal.invert_y} /> {t('Invert Y')}</label>
            <label className="check"><input name="diagonals" type="checkbox" defaultChecked={signal.diagonals} /> {t('Navigation diagonals')}</label>
            <label className="check"><input name="ai" type="checkbox" defaultChecked={profile?.ai_enabled ?? true} /> {t('Enable Claude')}</label>
          </div>
          <button type="submit">{t('Save profile')}</button>
        </form>
      </section>
    </div>
  </>;
}

// A person's devices: joysticks give input; Android phones and tablets show
// the panel. One phone is primary: it taps, speaks and reads the screen,
// the others mirror the panel. New devices wait here until assigned.
function ProfileDevices({ api, run, devices, profile, reload, moving }) {
  const [, tick] = useState(0);
  useEffect(() => {
    const timer = setInterval(() => tick((n) => n + 1), 1000);
    return () => clearInterval(timer);
  }, []);
  const mine = devices.filter((d) => d.profile_id === profile.id);
  const waiting = devices.filter((d) => !d.profile_id);
  const others = devices.filter((d) => d.profile_id && d.profile_id !== profile.id);
  const patch = (d, body) => run(async () => { await api('/devices/' + encodeURIComponent(d.id), 'PATCH', body); await reload(); });
  const forget = (d) => run(async () => { await api('/devices/' + encodeURIComponent(d.id), 'DELETE'); await reload(); });
  const primary = (d) => run(async () => { await api('/profiles/' + profile.id + '/primary', 'PUT', { device_id: d.id }); await reload(); });
  const kindLabel = (d) => t(d.kind === 'client' ? 'Android' : 'Joystick');
  const isMoving = (d) => Date.now() - (moving[d.id] || 0) < 3000;
  const name = (d) => <input aria-label={t('Name of {id}', { id: d.id })} defaultValue={d.name} placeholder={d.id}
    onBlur={(e) => e.target.value.trim() !== d.name && patch(d, { name: e.target.value.trim() })} />;
  const status = (d) => <span className={'dot ' + (d.status === 'online' ? 'on' : 'off')} title={t(d.status)} aria-label={t(d.status)} />;
  const group = (kind, title, empty) => {
    const list = mine.filter((d) => d.kind === kind);
    return <>
      <h3>{title}</h3>
      {list.length === 0 ? <p>{empty}</p> : <ul className="device-list">{list.map((d) => <li key={d.id}>
        {status(d)}{name(d)}
        {kind === 'client' && <label className="check"><input type="radio" name={'primary-' + profile.id} checked={profile.primary_client === d.id}
          onChange={() => primary(d)} /> {t('Acts on the screen')}</label>}
        <button onClick={() => patch(d, { profile_id: null })}>{t('Remove')}</button>
      </li>)}</ul>}
    </>;
  };
  return <section className="panel">
    <h2>{t("{name}'s devices", { name: profile.name })}</h2>
    {group('joystick', t('Joysticks'), t('No joystick yet. Turn one on, then add it below.'))}
    {group('client', t('Android'), t('No phone yet. Open the ActSpeed app on the phone and pick this profile, or add it below.'))}
    <h3>{t('New devices')}</h3>
    {waiting.length === 0 ? <p>{t('None waiting. A joystick shows up here when it first connects; move it to tell which one it is.')}</p>
      : <ul className="device-list">{waiting.map((d) => <li key={d.id}>
        {status(d)}<span className="kind">{kindLabel(d)}</span>{name(d)}
        {isMoving(d) && <span className="tag">{t('moving now')}</span>}
        <button onClick={() => patch(d, { profile_id: profile.id })}>{t('Add to {name}', { name: profile.name })}</button>
        <button onClick={() => forget(d)}>{t('Forget')}</button>
      </li>)}</ul>}
    {others.length > 0 && <details>
      <summary>{t('Devices of other profiles ({n})', { n: others.length })}</summary>
      <ul className="device-list">{others.map((d) => <li key={d.id}>
        {status(d)}<span className="kind">{kindLabel(d)}</span><span>{d.name || d.id}</span>
        <button onClick={() => patch(d, { profile_id: profile.id })}>{t('Move to {name}', { name: profile.name })}</button>
      </li>)}</ul>
    </details>}
  </section>;
}

function PatternForm({ initial = {}, profiles, onSave, onCancel, recorded = false }) {
  return <form onSubmit={(event) => {
    event.preventDefault();
    const fields = new FormData(event.currentTarget);
    const pattern = { ...initial, ...Object.fromEntries(fields), profile_id: fields.get('profile_id') || null, enabled: fields.has('enabled') };
    if (!recorded) pattern.sequence = gestureCodes(pattern.sequence);
    onSave(pattern);
  }}>
    <div className="fields">
      {!recorded && <label>{t('Gesture sequence')}<input name="sequence" required placeholder="↖ ↗" defaultValue={gestureGlyphs(initial.sequence)} /></label>}
      <label>{t('Mode')}<select name="mode" defaultValue={initial.mode || 'text.corners'}>
        {modes.map((mode) => <option key={mode}>{mode}</option>)}
      </select></label>
      <label>{t('Output kind')}<select name="output_kind" defaultValue={initial.output_kind || 'char'}>
        {['char', 'phrase', 'control', 'action'].map((kind) => <option key={kind}>{kind}</option>)}
      </select></label>
      <label>{t('Output value')}<input name="output_value" required defaultValue={initial.output_value} /></label>
      <label>{t('Profile')}<select name="profile_id" defaultValue={initial.profile_id || ''}>
        <option value="">{t('All profiles')}</option>
        {profiles.map((profile) => <option key={profile.id} value={profile.id}>{profile.name}</option>)}
      </select></label>
    </div>
    <label className="check"><input type="checkbox" name="enabled" defaultChecked={initial.enabled ?? true} /> {t('Enabled')}</label>
    <div className="actions"><button type="submit">{t('Save pattern')}</button>
      {onCancel && <button type="button" onClick={onCancel}>{t('Cancel')}</button>}</div>
  </form>;
}

export function Patterns({ api, run }) {
  const [patterns, reload] = useList('/patterns', api, run);
  const [profiles] = useList('/profiles', api, run);
  const [editing, setEditing] = useState(null);
  const [filter, setFilter] = useState('');
  const visible = patterns.filter((pattern) => (!filter || pattern.mode === filter) && !/(^| )B\+?( |$)/.test(pattern.sequence));
  const save = (body) => run(async () => {
    await api('/patterns' + (editing ? '/' + editing.id : ''), editing ? 'PUT' : 'POST', body);
    await reload();
    setEditing(null);
  });
  return <>
    <h1>{t('Patterns')}</h1>
    <section className="panel">
      <h2>{editing ? <>{t('Edit')} <GestureSymbols sequence={editing.sequence} /></> : t('Add a pattern')}</h2>
      <PatternForm key={editing?.id || 'new'} initial={editing || undefined} profiles={profiles} onSave={save}
        onCancel={editing ? () => setEditing(null) : undefined} />
    </section>
    <section className="panel">
      <div className="panel-head"><h2>{t('Gesture library')}</h2>
        <label>{t('Filter by mode')}<select value={filter} onChange={(e) => setFilter(e.target.value)}>
          <option value="">{t('All modes')}</option>{modes.map((mode) => <option key={mode}>{mode}</option>)}
        </select></label></div>
      <ul className="records">{visible.map((pattern) => <li key={pattern.id}>
        <div><strong><GestureSymbols sequence={pattern.sequence} /></strong> → {pattern.output_value}<br /><small>{pattern.mode} · {t(pattern.enabled ? 'Enabled' : 'Disabled')}</small></div>
        <div className="actions">
          <button onClick={() => {
            setEditing(pattern);
            requestAnimationFrame(() => document.querySelector('input[name=sequence]')?.focus());
          }} aria-label={t('Edit') + ' ' + gestureName(pattern.sequence) + ' · ' + pattern.mode}>{t('Edit')}</button>
          <button onClick={() => run(async () => {
            await api('/patterns/' + pattern.id, 'PUT', { ...pattern, enabled: !pattern.enabled });
            await reload();
          })} aria-label={t(pattern.enabled ? 'Disable' : 'Enable') + ' ' + gestureName(pattern.sequence) + ' · ' + pattern.mode}>
            {t(pattern.enabled ? 'Disable' : 'Enable')}
          </button>
        </div>
      </li>)}</ul>
    </section>
  </>;
}

export function Phrases({ api, run, language }) {
  const [phrases, reload] = useList('/phrases?language=' + language, api, run);
  const [text, setText] = useState('');
  return <>
    <h1>{t('Phrase bank')}</h1>
    <p>{t('Shared phrases in {language}, plus every profile\'s own.', { language: languages[language] })}</p>
    <section className="panel">
      <form onSubmit={(e) => {
        e.preventDefault();
        run(async () => { await api('/phrases', 'POST', { text, language }); setText(''); await reload(); });
      }}>
        <label>{t('New phrase')}<input value={text} onChange={(e) => setText(e.target.value)} required /></label>
        <button type="submit">{t('Add phrase')}</button>
      </form>
      <ul className="records">{phrases.map((phrase) => <li key={phrase.id}>
        <span>{phrase.text}</span>
        <button aria-label={t('Remove phrase: {text}', { text: phrase.text })} onClick={() => run(async () => {
          await api('/phrases/' + phrase.id, 'DELETE'); await reload();
        })}>{t('Remove')}</button>
      </li>)}</ul>
    </section>
  </>;
}

export function Suggestions({ api, run }) {
  const [items, reload] = useList('/suggestions', api, run);
  const resolve = (id, action) => run(async () => { await api('/suggestions/' + id + '/' + action, 'POST'); await reload(); });
  return <>
    <div className="page-title"><h1>{t('AI suggestions')}</h1><button onClick={() => run(reload)}>{t('Refresh suggestions')}</button></div>
    <section className="panel">
      {items.length === 0 && <p>{t('No suggestions yet.')}</p>}
      <ul className="records">{items.map((item) => <li key={item.id}>
        <div><strong>{item.kind === 'interpret' ? <GestureSymbols sequence={item.input} /> : item.input}</strong> → {item.candidates?.[0]?.value || '—'}<br />
          <small>{t(item.status.replaceAll('_', ' '))} · {item.model}</small></div>
        <div className="actions">
          {item.status === 'pending' && <>
            <button onClick={() => resolve(item.id, 'accept')} aria-label={t('Accept') + ': ' + (item.kind === 'interpret' ? gestureName(item.input) : item.input)}>{t('Accept')}</button>
            <button onClick={() => resolve(item.id, 'reject')} aria-label={t('Reject') + ': ' + (item.kind === 'interpret' ? gestureName(item.input) : item.input)}>{t('Reject')}</button>
          </>}
          {item.kind === 'interpret' && ['accepted', 'auto_applied'].includes(item.status) && !item.pattern_id &&
            <button onClick={() => resolve(item.id, 'promote')}>{t('Save as pattern')}</button>}
        </div>
      </li>)}</ul>
    </section>
  </>;
}

export function Recordings({ api, run }) {
  const [items, reload] = useList('/recordings', api, run);
  const [profiles] = useList('/profiles', api, run);
  const [chosen, setChosen] = useState(null);
  return <>
    <div className="page-title"><h1>{t('Recordings')}</h1><button onClick={() => run(reload)}>{t('Refresh recordings')}</button></div>
    <section className="panel">
      {items.length === 0 && <p>{t('Start a recording on the Live page.')}</p>}
      <ul className="records">{items.map((item) => <li key={item.id}>
        <div><strong>{item.label || item.device_id}</strong><br /><small>{item.device_id} · {t(item.status)}</small>
          <div className="actions">{item.gestures?.map((sequence, index) => <button key={index} onClick={() => setChosen({ item, index, sequence })}>
            <GestureSymbols sequence={sequence} />
          </button>)}</div></div>
        {item.status === 'recording' && <button onClick={() => run(async () => {
          await api('/recordings/' + item.id + '/stop', 'POST'); await reload();
        })}>{t('Stop recording')}</button>}
      </li>)}</ul>
    </section>
    {chosen && <section className="panel">
      <h2>{t('Map recorded gesture')} <GestureSymbols sequence={chosen.sequence} /></h2>
      <PatternForm key={chosen.item.id + chosen.index} profiles={profiles} recorded
        onCancel={() => setChosen(null)} onSave={(pattern) => run(async () => {
          await api('/recordings/' + chosen.item.id + '/pattern', 'POST', { ...pattern, index: chosen.index });
          setChosen(null);
        })} />
    </section>}
  </>;
}
