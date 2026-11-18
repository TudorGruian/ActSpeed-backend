import React from 'react';
import { t } from './i18n.js';

const symbols = {
  N: ['↑', 'up'],
  NE: ['↗', 'up-right'],
  E: ['→', 'right'],
  SE: ['↘', 'down-right'],
  S: ['↓', 'down'],
  SW: ['↙', 'down-left'],
  W: ['←', 'left'],
  NW: ['↖', 'up-left'],
  C: ['○', 'center'],
  CW: ['↻', 'clockwise circle'],
  CCW: ['↺', 'counterclockwise circle'],
};

const codes = Object.fromEntries(Object.entries(symbols).map(([code, [glyph]]) => [glyph, code]));
const glyphPattern = new RegExp('[' + Object.keys(codes).join('') + ']\\+?', 'gu');
const tokens = (sequence) => (sequence || '').trim().split(/\s+/).filter(Boolean);

function formatToken(token, index) {
  const held = token.endsWith('+');
  const entry = symbols[held ? token.slice(0, -1) : token];
  if (!entry) return token;
  if (index === 0) return entry[0] + (held ? '+' : '');
  return t(entry[1]) + (held ? ' ' + t('held') : '');
}

export const gestureGlyphs = (sequence) => tokens(sequence).map((token) => formatToken(token, 0)).join(' ');
export const gestureName = (sequence) => tokens(sequence).map((token) => formatToken(token, 1)).join(t(', then '));

export function gestureCodes(sequence) {
  return sequence.replace(glyphPattern, (match) => ' ' + codes[match[0]] + match.slice(1) + ' ')
    .trim().split(/\s+/).join(' ');
}

export function GestureSymbols({ sequence }) {
  return <span className="gesture-symbols" role="img" aria-label={gestureName(sequence)}>{gestureGlyphs(sequence)}</span>;
}
