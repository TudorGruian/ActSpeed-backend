-- +goose Up
UPDATE patterns
SET output_value = 'text.speak_as_typed'
WHERE profile_id IS NULL
  AND mode = 'text.corners'
  AND sequence = 'S'
  AND output_kind = 'control'
  AND output_value = 'text.speak'
  AND source = 'builtin';
