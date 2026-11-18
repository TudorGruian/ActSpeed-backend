-- +goose Up
-- Shared starter phrases come per language; a profile sees the ones in its
-- language plus its own. NULL = every language (phrases added by hand).
ALTER TABLE phrases ADD COLUMN language text;
UPDATE phrases SET language = 'en'
WHERE profile_id IS NULL AND text IN ('Yes', 'No', 'Thank you', 'Please help me', 'I''m thirsty', 'I''m hungry',
    'I''m in pain', 'I need the toilet', 'I''m tired', 'Please wait');
INSERT INTO phrases (text, sort_order, language)
SELECT t, n, 'ro' FROM (VALUES
    ('Da', 0), ('Nu', 1), ('Mulțumesc', 2), ('Te rog, ajută-mă', 3), ('Mi-e sete', 4), ('Mi-e foame', 5),
    ('Mă doare', 6), ('Trebuie să merg la toaletă', 7), ('Vreau să mă odihnesc', 8), ('Te rog, așteaptă', 9)
) AS v(t, n)
WHERE EXISTS (SELECT 1 FROM phrases WHERE language = 'en');

-- +goose Down
DELETE FROM phrases WHERE language = 'ro' AND profile_id IS NULL;
ALTER TABLE phrases DROP COLUMN language;
