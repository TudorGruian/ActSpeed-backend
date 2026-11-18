-- +goose Up
CREATE TABLE memories (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    device_id  text NOT NULL REFERENCES devices (id) ON DELETE CASCADE,
    profile_id uuid REFERENCES profiles (id) ON DELETE CASCADE,
    kind       text NOT NULL CHECK (kind IN ('screen', 'action', 'text', 'note')),
    app        text NOT NULL DEFAULT '',
    content    text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX memories_profile_idx ON memories (profile_id, created_at DESC);
CREATE INDEX memories_device_idx ON memories (device_id, created_at DESC);

CREATE TABLE assists (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    device_id   text NOT NULL REFERENCES devices (id) ON DELETE CASCADE,
    profile_id  uuid REFERENCES profiles (id) ON DELETE SET NULL,
    app         text NOT NULL DEFAULT '',
    summary     text NOT NULL DEFAULT '',
    proposals   jsonb NOT NULL DEFAULT '[]',
    model       text NOT NULL DEFAULT '',
    status      text NOT NULL DEFAULT 'active',
    chosen      integer,
    width       integer NOT NULL DEFAULT 0,
    height      integer NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz
);
CREATE INDEX assists_device_idx ON assists (device_id, created_at DESC);

-- Existing installations get the "look at the screen" gesture; new ones
-- receive it from the seed.
INSERT INTO patterns (mode, sequence, output_kind, output_value, source)
SELECT 'nav', 'N S', 'control', 'assist.look', 'builtin'
WHERE EXISTS (SELECT 1 FROM patterns WHERE source = 'builtin')
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM patterns WHERE source = 'builtin' AND output_value = 'assist.look';
DROP TABLE assists, memories;
