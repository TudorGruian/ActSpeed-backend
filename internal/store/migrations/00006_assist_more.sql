-- +goose Up
-- "More options" for the screen assistant: a circle and back, in gesture
-- navigation (pointer navigation uses it for scroll mode).
INSERT INTO patterns (mode, sequence, output_kind, output_value, source)
SELECT 'nav.gesture', 'CW CCW', 'control', 'assist.more', 'builtin'
WHERE EXISTS (SELECT 1 FROM patterns WHERE source = 'builtin')
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM patterns WHERE source = 'builtin' AND output_value = 'assist.more';
