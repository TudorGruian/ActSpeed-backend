-- +goose Up
UPDATE patterns
SET output_value = 'text.correct_and_speak'
WHERE profile_id IS NULL
  AND mode = 'text'
  AND sequence = 'CCW'
  AND output_kind = 'control'
  AND output_value = 'text.speak'
  AND source = 'builtin';
