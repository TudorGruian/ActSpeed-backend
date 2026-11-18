-- +goose Up
CREATE TABLE profiles (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name         text NOT NULL,
    input_method text NOT NULL DEFAULT 'mouth' CHECK (input_method IN ('mouth', 'thumb')),
    language     text NOT NULL DEFAULT 'en',
    ai_enabled   boolean NOT NULL DEFAULT true,
    settings     jsonb NOT NULL DEFAULT '{}',
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE devices (
    id           text PRIMARY KEY,
    profile_id   uuid REFERENCES profiles (id) ON DELETE SET NULL,
    name         text NOT NULL DEFAULT '',
    fw_version   text NOT NULL DEFAULT '',
    status       text NOT NULL DEFAULT 'unknown',
    mode         text NOT NULL DEFAULT 'nav' CHECK (mode IN ('nav', 'text')),
    paused       boolean NOT NULL DEFAULT false,
    last_seen_at timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE actions (
    key      text PRIMARY KEY,
    label    text NOT NULL,
    category text NOT NULL DEFAULT '',
    params   jsonb,
    builtin  boolean NOT NULL DEFAULT false
);

CREATE TABLE patterns (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    profile_id    uuid REFERENCES profiles (id) ON DELETE CASCADE,
    mode          text NOT NULL,
    sequence      text NOT NULL,
    output_kind   text NOT NULL CHECK (output_kind IN ('action', 'char', 'phrase', 'control')),
    output_value  text NOT NULL,
    output_params jsonb,
    source        text NOT NULL DEFAULT 'manual',
    enabled       boolean NOT NULL DEFAULT true,
    created_at    timestamptz NOT NULL DEFAULT now()
);
-- One meaning per gesture per scope and mode.
CREATE UNIQUE INDEX patterns_scope_idx ON patterns
    (COALESCE(profile_id, '00000000-0000-0000-0000-000000000000'::uuid), mode, sequence);

CREATE TABLE phrases (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    profile_id uuid REFERENCES profiles (id) ON DELETE CASCADE,
    text       text NOT NULL,
    sort_order integer NOT NULL DEFAULT 0
);

CREATE TABLE suggestions (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    device_id   text NOT NULL REFERENCES devices (id) ON DELETE CASCADE,
    profile_id  uuid REFERENCES profiles (id) ON DELETE SET NULL,
    kind        text NOT NULL CHECK (kind IN ('interpret', 'correct')),
    context     text NOT NULL,
    input       text NOT NULL,
    candidates  jsonb NOT NULL DEFAULT '[]',
    model       text NOT NULL DEFAULT '',
    status      text NOT NULL DEFAULT 'pending',
    chosen      text NOT NULL DEFAULT '',
    pattern_id  uuid REFERENCES patterns (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz
);
CREATE INDEX suggestions_status_idx ON suggestions (status, created_at DESC);

CREATE TABLE utterances (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    device_id  text NOT NULL REFERENCES devices (id) ON DELETE CASCADE,
    profile_id uuid REFERENCES profiles (id) ON DELETE SET NULL,
    composed   text NOT NULL,
    final      text NOT NULL,
    corrected  boolean NOT NULL DEFAULT false,
    audio_file text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX utterances_profile_idx ON utterances (profile_id, created_at DESC);

CREATE TABLE recordings (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    device_id  text NOT NULL REFERENCES devices (id) ON DELETE CASCADE,
    label      text NOT NULL DEFAULT '',
    status     text NOT NULL DEFAULT 'recording',
    gestures   jsonb NOT NULL DEFAULT '[]',
    started_at timestamptz NOT NULL DEFAULT now(),
    ended_at   timestamptz
);

-- +goose Down
DROP TABLE recordings, utterances, suggestions, phrases, patterns, actions, devices, profiles;
