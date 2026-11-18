-- +goose Up
-- Existing profiles and custom bindings are preserved.
INSERT INTO patterns (mode, sequence, output_kind, output_value, source)
SELECT mode, sequence, kind, value, 'builtin'
FROM (VALUES
    ('any', 'CW', 'control', 'control.mode_toggle'),
    ('any', 'CW CW', 'control', 'control.pause_toggle'),
    ('any', 'CCW CCW', 'control', 'alert.call'),
    ('nav.pointer', 'CCW', 'action', 'pointer.click'),
    ('nav.pointer', 'CCW CW', 'action', 'pointer.double'),
    ('nav.pointer', 'CCW CW CW', 'action', 'pointer.right'),
    ('nav.pointer', 'CCW CW CCW', 'action', 'pointer.drag_lock'),
    ('nav.pointer', 'CW CCW CW', 'control', 'control.speed_cycle'),
    ('nav.pointer', 'CW CCW', 'control', 'control.scroll_toggle'),
    ('nav.gesture', 'CCW', 'action', 'key.enter'),
    ('nav.gesture', 'CCW CW', 'action', 'key.escape'),
    ('text', 'CCW', 'control', 'text.speak'),
    ('text.grid', 'CCW', 'control', 'grid.select'),
    ('text.twodir', 'CW CCW', 'control', 'text.space')
) AS bindings(mode, sequence, kind, value)
WHERE EXISTS (SELECT 1 FROM patterns WHERE source = 'builtin')
ON CONFLICT DO NOTHING;
