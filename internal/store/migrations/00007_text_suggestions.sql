-- +goose Up
-- Corner alphabet: hold up for the second word, hold right or down for the
-- first or second suggested phrase.
INSERT INTO patterns (mode, sequence, output_kind, output_value, output_params, source)
SELECT v.mode, v.seq, 'control', v.val, v.params::jsonb, 'builtin'
FROM (VALUES
    ('text.corners', 'N+', 'text.accept_prediction', '{"index":1}'),
    ('text.corners', 'E+', 'text.accept_phrase', '{"index":0}'),
    ('text.corners', 'S+', 'text.accept_phrase', '{"index":1}')
) AS v(mode, seq, val, params)
WHERE EXISTS (SELECT 1 FROM patterns WHERE source = 'builtin')
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM patterns WHERE source = 'builtin' AND mode = 'text.corners' AND sequence IN ('N+', 'E+', 'S+');
