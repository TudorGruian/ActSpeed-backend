-- +goose Up
-- A profile is the person. It owns joysticks (input) and clients (Android
-- phones and tablets that show the panel and act on the screen). The live
-- state (mode, pause) belongs to the person, and one client is primary: it
-- carries out taps, speech and screen readings; the others mirror the panel.
ALTER TABLE devices ADD COLUMN kind text NOT NULL DEFAULT 'joystick'
    CHECK (kind IN ('joystick', 'client'));

ALTER TABLE profiles
    ADD COLUMN mode           text NOT NULL DEFAULT 'nav' CHECK (mode IN ('nav', 'text')),
    ADD COLUMN paused         boolean NOT NULL DEFAULT false,
    ADD COLUMN primary_client text REFERENCES devices (id) ON DELETE SET NULL;

UPDATE profiles p SET mode = d.mode, paused = d.paused
FROM (SELECT DISTINCT ON (profile_id) profile_id, mode, paused
      FROM devices WHERE profile_id IS NOT NULL
      ORDER BY profile_id, last_seen_at DESC NULLS LAST) d
WHERE d.profile_id = p.id;

ALTER TABLE devices DROP COLUMN mode, DROP COLUMN paused;

-- History belongs to the person: forgetting a device keeps it.
ALTER TABLE suggestions ALTER COLUMN device_id DROP NOT NULL,
    DROP CONSTRAINT suggestions_device_id_fkey,
    ADD CONSTRAINT suggestions_device_id_fkey FOREIGN KEY (device_id) REFERENCES devices (id) ON DELETE SET NULL;
ALTER TABLE utterances ALTER COLUMN device_id DROP NOT NULL,
    DROP CONSTRAINT utterances_device_id_fkey,
    ADD CONSTRAINT utterances_device_id_fkey FOREIGN KEY (device_id) REFERENCES devices (id) ON DELETE SET NULL;
ALTER TABLE memories ALTER COLUMN device_id DROP NOT NULL,
    DROP CONSTRAINT memories_device_id_fkey,
    ADD CONSTRAINT memories_device_id_fkey FOREIGN KEY (device_id) REFERENCES devices (id) ON DELETE SET NULL;
ALTER TABLE assists ALTER COLUMN device_id DROP NOT NULL,
    DROP CONSTRAINT assists_device_id_fkey,
    ADD CONSTRAINT assists_device_id_fkey FOREIGN KEY (device_id) REFERENCES devices (id) ON DELETE SET NULL;
ALTER TABLE recordings ALTER COLUMN device_id DROP NOT NULL,
    DROP CONSTRAINT recordings_device_id_fkey,
    ADD CONSTRAINT recordings_device_id_fkey FOREIGN KEY (device_id) REFERENCES devices (id) ON DELETE SET NULL,
    ADD COLUMN profile_id uuid REFERENCES profiles (id) ON DELETE CASCADE;
UPDATE recordings r SET profile_id = d.profile_id FROM devices d WHERE d.id = r.device_id;

-- +goose Down
ALTER TABLE recordings DROP COLUMN profile_id;
ALTER TABLE devices ADD COLUMN mode text NOT NULL DEFAULT 'nav' CHECK (mode IN ('nav', 'text')),
    ADD COLUMN paused boolean NOT NULL DEFAULT false;
UPDATE devices d SET mode = p.mode, paused = p.paused FROM profiles p WHERE p.id = d.profile_id;
ALTER TABLE profiles DROP COLUMN primary_client, DROP COLUMN paused, DROP COLUMN mode;
ALTER TABLE devices DROP COLUMN kind;
