// Package store is the PostgreSQL persistence layer.
package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/actspeed/actspeed-backend/internal/gesture"
	"github.com/actspeed/actspeed-backend/internal/model"
	"github.com/actspeed/actspeed-backend/internal/seed"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Errors returned to callers.
var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
	ErrInvalid  = errors.New("invalid reference")
)

// Store wraps a connection pool.
type Store struct {
	pool *pgxpool.Pool
}

// Open connects, migrates the schema and seeds built-in data.
func Open(ctx context.Context, url string) (*Store, error) {
	if err := migrate(ctx, url); err != nil {
		return nil, err
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("store: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	s := &Store{pool: pool}
	if err := s.seed(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: seed: %w", err)
	}
	return s, nil
}

func migrate(ctx context.Context, url string) error {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return fmt.Errorf("store: open for migration: %w", err)
	}
	defer db.Close()
	goose.SetBaseFS(migrations)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	if err := goose.UpContext(ctx, db, "migrations"); err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	return nil
}

// Close releases the pool.
func (s *Store) Close() { s.pool.Close() }

// Ping checks connectivity.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// seed inserts built-in actions, patterns and phrases that are missing.
// Existing rows are never overwritten, so edits made through the API stick.
func (s *Store) seed(ctx context.Context) error {
	for _, a := range seed.Actions() {
		if _, err := s.pool.Exec(ctx,
			`INSERT INTO actions (key, label, category, params, builtin) VALUES ($1,$2,$3,$4,true)
			 ON CONFLICT (key) DO NOTHING`, a.Key, a.Label, a.Category, nullJSON(a.Params)); err != nil {
			return err
		}
	}
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM patterns WHERE source = 'builtin'`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		for _, p := range seed.Patterns() {
			if _, err := s.pool.Exec(ctx,
				`INSERT INTO patterns (mode, sequence, output_kind, output_value, source)
				 VALUES ($1,$2,$3,$4,'builtin') ON CONFLICT DO NOTHING`,
				p.Mode, p.Sequence, p.OutputKind, p.OutputValue); err != nil {
				return err
			}
		}
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM phrases`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		for lang, list := range seed.Phrases() {
			for i, t := range list {
				if _, err := s.pool.Exec(ctx, `INSERT INTO phrases (text, sort_order, language) VALUES ($1,$2,$3)`, t, i, lang); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func nullJSON(b json.RawMessage) any {
	if len(b) == 0 {
		return nil
	}
	return []byte(b)
}

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case "23505":
			return ErrConflict
		case "23503", "22P02", "23514":
			return fmt.Errorf("%w: %s", ErrInvalid, pg.Message)
		}
	}
	return err
}

func affected(tag pgconn.CommandTag, err error) error {
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- profiles -----------------------------------------------------------

const profileCols = `id::text, name, input_method, language, ai_enabled, settings, mode, paused, primary_client, created_at, updated_at`

func scanProfile(row pgx.Row) (model.Profile, error) {
	var p model.Profile
	var settings []byte
	if err := row.Scan(&p.ID, &p.Name, &p.InputMethod, &p.Language, &p.AIEnabled, &settings, &p.Mode, &p.Paused, &p.PrimaryClient, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return p, mapErr(err)
	}
	_ = json.Unmarshal(settings, &p.Settings)
	return p, nil
}

func (s *Store) ListProfiles(ctx context.Context) ([]model.Profile, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+profileCols+` FROM profiles ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Profile{}
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetProfile(ctx context.Context, id string) (model.Profile, error) {
	return scanProfile(s.pool.QueryRow(ctx, `SELECT `+profileCols+` FROM profiles WHERE id = $1`, id))
}

func (s *Store) CreateProfile(ctx context.Context, p model.Profile) (model.Profile, error) {
	settings, _ := json.Marshal(p.Settings)
	return scanProfile(s.pool.QueryRow(ctx,
		`INSERT INTO profiles (name, input_method, language, ai_enabled, settings)
		 VALUES ($1,$2,$3,$4,$5) RETURNING `+profileCols,
		p.Name, p.InputMethod, p.Language, p.AIEnabled, settings))
}

func (s *Store) UpdateProfile(ctx context.Context, p model.Profile) (model.Profile, error) {
	settings, _ := json.Marshal(p.Settings)
	return scanProfile(s.pool.QueryRow(ctx,
		`UPDATE profiles SET name=$2, input_method=$3, language=$4, ai_enabled=$5, settings=$6, updated_at=now()
		 WHERE id=$1 RETURNING `+profileCols,
		p.ID, p.Name, p.InputMethod, p.Language, p.AIEnabled, settings))
}

// SetProfileGap stores a learned gap_ms without touching other settings.
func (s *Store) SetProfileGap(ctx context.Context, id string, gapMs int) error {
	return affected(s.pool.Exec(ctx,
		`UPDATE profiles SET settings = jsonb_set(settings, '{gap_ms}', to_jsonb($2::int)), updated_at = now() WHERE id = $1`,
		id, gapMs))
}

func (s *Store) DeleteProfile(ctx context.Context, id string) error {
	return affected(s.pool.Exec(ctx, `DELETE FROM profiles WHERE id=$1`, id))
}

// ---- devices ------------------------------------------------------------

const deviceCols = `id, profile_id::text, name, kind, fw_version, status, last_seen_at, created_at`

func scanDevice(row pgx.Row) (model.Device, error) {
	var d model.Device
	err := row.Scan(&d.ID, &d.ProfileID, &d.Name, &d.Kind, &d.FWVersion, &d.Status, &d.LastSeenAt, &d.CreatedAt)
	return d, mapErr(err)
}

// TouchDevice registers a device on first contact and returns it. The kind
// is set when the device is first seen and kept afterwards.
func (s *Store) TouchDevice(ctx context.Context, id, kind string) (model.Device, error) {
	return scanDevice(s.pool.QueryRow(ctx,
		`INSERT INTO devices (id, kind, last_seen_at) VALUES ($1, $2, now())
		 ON CONFLICT (id) DO UPDATE SET last_seen_at = now() RETURNING `+deviceCols, id, kind))
}

// SetDeviceStatus records an online/offline report and firmware version.
// Status reports come from joysticks; clients register through the API.
func (s *Store) SetDeviceStatus(ctx context.Context, id, status, fw string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO devices (id, status, fw_version, last_seen_at) VALUES ($1,$2,$3,now())
		 ON CONFLICT (id) DO UPDATE SET status = $2, last_seen_at = now(),
		   fw_version = CASE WHEN $3 = '' THEN devices.fw_version ELSE $3 END`, id, status, fw)
	return mapErr(err)
}

// SetProfileState stores the person's mode and pause state.
func (s *Store) SetProfileState(ctx context.Context, id, mode string, paused bool) error {
	_, err := s.pool.Exec(ctx, `UPDATE profiles SET mode=$2, paused=$3 WHERE id=$1`, id, mode, paused)
	return mapErr(err)
}

func (s *Store) ListDevices(ctx context.Context) ([]model.Device, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+deviceCols+` FROM devices ORDER BY kind, name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Device{}
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) GetDevice(ctx context.Context, id string) (model.Device, error) {
	return scanDevice(s.pool.QueryRow(ctx, `SELECT `+deviceCols+` FROM devices WHERE id=$1`, id))
}

// fixPrimary keeps a profile's primary client valid: if it has none, or the
// one it has left the profile, the most recently seen client takes over.
const fixPrimary = `UPDATE profiles SET primary_client = (
	SELECT id FROM devices WHERE profile_id = $1 AND kind = 'client'
	ORDER BY last_seen_at DESC NULLS LAST, id LIMIT 1)
 WHERE id = $1 AND (primary_client IS NULL OR primary_client NOT IN (
	SELECT id FROM devices WHERE profile_id = $1 AND kind = 'client'))`

// UpdateDevice sets the display name, kind and profile assignment, and keeps
// the primary client of the profiles involved valid.
func (s *Store) UpdateDevice(ctx context.Context, id, name, kind string, profileID *string) (model.Device, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return model.Device{}, err
	}
	defer tx.Rollback(ctx)
	var old *string
	if err := tx.QueryRow(ctx, `SELECT profile_id::text FROM devices WHERE id=$1 FOR UPDATE`, id).Scan(&old); err != nil {
		return model.Device{}, mapErr(err)
	}
	d, err := scanDevice(tx.QueryRow(ctx,
		`UPDATE devices SET name=$2, kind=$3, profile_id=$4 WHERE id=$1 RETURNING `+deviceCols, id, name, kind, profileID))
	if err != nil {
		return d, err
	}
	for _, p := range []*string{old, profileID} {
		if p != nil {
			if _, err := tx.Exec(ctx, fixPrimary, *p); err != nil {
				return d, mapErr(err)
			}
		}
	}
	return d, mapErr(tx.Commit(ctx))
}

// DeleteDevice forgets a device. Its profile keeps the history.
func (s *Store) DeleteDevice(ctx context.Context, id string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var pid *string
	if err := tx.QueryRow(ctx, `DELETE FROM devices WHERE id=$1 RETURNING profile_id::text`, id).Scan(&pid); err != nil {
		return mapErr(err)
	}
	if pid != nil {
		if _, err := tx.Exec(ctx, fixPrimary, *pid); err != nil {
			return mapErr(err)
		}
	}
	return mapErr(tx.Commit(ctx))
}

// SetPrimaryClient makes one of the profile's clients the one that acts.
func (s *Store) SetPrimaryClient(ctx context.Context, profileID, deviceID string) (model.Profile, error) {
	return scanProfile(s.pool.QueryRow(ctx,
		`UPDATE profiles SET primary_client = $2 WHERE id = $1
		   AND EXISTS (SELECT 1 FROM devices WHERE id = $2 AND profile_id = $1 AND kind = 'client')
		 RETURNING `+profileCols, profileID, deviceID))
}

// ---- actions ------------------------------------------------------------

func (s *Store) ListActions(ctx context.Context) ([]model.Action, error) {
	rows, err := s.pool.Query(ctx, `SELECT key, label, category, params, builtin FROM actions ORDER BY category, key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Action{}
	for rows.Next() {
		var a model.Action
		var params []byte
		if err := rows.Scan(&a.Key, &a.Label, &a.Category, &params, &a.Builtin); err != nil {
			return nil, err
		}
		a.Params = params
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) UpsertAction(ctx context.Context, a model.Action) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO actions (key, label, category, params) VALUES ($1,$2,$3,$4)
		 ON CONFLICT (key) DO UPDATE SET label=$2, category=$3, params=$4`,
		a.Key, a.Label, a.Category, nullJSON(a.Params))
	return mapErr(err)
}

func (s *Store) DeleteAction(ctx context.Context, key string) error {
	return affected(s.pool.Exec(ctx, `DELETE FROM actions WHERE key=$1`, key))
}

// ---- patterns -----------------------------------------------------------

const patternCols = `id::text, profile_id::text, mode, sequence, output_kind, output_value, output_params, source, enabled, created_at`

func scanPattern(row pgx.Row) (model.Pattern, error) {
	var p model.Pattern
	var params []byte
	err := row.Scan(&p.ID, &p.ProfileID, &p.Mode, &p.Sequence, &p.OutputKind, &p.OutputValue, &params, &p.Source, &p.Enabled, &p.CreatedAt)
	p.OutputParams = params
	return p, mapErr(err)
}

func (s *Store) ListPatterns(ctx context.Context) ([]model.Pattern, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+patternCols+` FROM patterns ORDER BY mode, sequence`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Pattern{}
	for rows.Next() {
		p, err := scanPattern(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) CreatePattern(ctx context.Context, p model.Pattern) (model.Pattern, error) {
	if p.Source == "" {
		p.Source = "manual"
	}
	return scanPattern(s.pool.QueryRow(ctx,
		`INSERT INTO patterns (profile_id, mode, sequence, output_kind, output_value, output_params, source, enabled)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING `+patternCols,
		p.ProfileID, p.Mode, gesture.NormalizeSequence(p.Sequence), p.OutputKind, p.OutputValue,
		nullJSON(p.OutputParams), p.Source, p.Enabled))
}

func (s *Store) UpdatePattern(ctx context.Context, p model.Pattern) (model.Pattern, error) {
	return scanPattern(s.pool.QueryRow(ctx,
		`UPDATE patterns SET profile_id=$2, mode=$3, sequence=$4, output_kind=$5, output_value=$6,
		   output_params=$7, enabled=$8 WHERE id=$1 RETURNING `+patternCols,
		p.ID, p.ProfileID, p.Mode, gesture.NormalizeSequence(p.Sequence), p.OutputKind, p.OutputValue,
		nullJSON(p.OutputParams), p.Enabled))
}

func (s *Store) DeletePattern(ctx context.Context, id string) error {
	return affected(s.pool.Exec(ctx, `DELETE FROM patterns WHERE id=$1`, id))
}

// ---- phrases ------------------------------------------------------------

// ListPhrases returns global phrases plus the profile's own. With
// all=true every phrase is returned regardless of owner.
// Shared phrases are limited to the language when one is given.
func (s *Store) ListPhrases(ctx context.Context, profileID *string, language string) ([]model.Phrase, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id::text, profile_id::text, text, sort_order, language FROM phrases
		 WHERE (profile_id IS NULL AND ($2 = '' OR language IS NULL OR language = $2)) OR profile_id = $1
		 ORDER BY sort_order, text`, profileID, language)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []model.Phrase{}
	for rows.Next() {
		var p model.Phrase
		if err := rows.Scan(&p.ID, &p.ProfileID, &p.Text, &p.SortOrder, &p.Language); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) CreatePhrase(ctx context.Context, p model.Phrase) (model.Phrase, error) {
	err := s.pool.QueryRow(ctx,
		`INSERT INTO phrases (profile_id, text, sort_order, language) VALUES ($1,$2,$3,$4) RETURNING id::text`,
		p.ProfileID, p.Text, p.SortOrder, p.Language).Scan(&p.ID)
	return p, mapErr(err)
}

func (s *Store) DeletePhrase(ctx context.Context, id string) error {
	return affected(s.pool.Exec(ctx, `DELETE FROM phrases WHERE id=$1`, id))
}

// ---- suggestions --------------------------------------------------------

const suggestionCols = `id::text, COALESCE(device_id, ''), profile_id::text, kind, context, input, candidates, model, status, chosen, pattern_id::text, created_at, resolved_at`

func scanSuggestion(row pgx.Row) (model.Suggestion, error) {
	var g model.Suggestion
	var cands []byte
	err := row.Scan(&g.ID, &g.DeviceID, &g.ProfileID, &g.Kind, &g.Context, &g.Input, &cands, &g.Model,
		&g.Status, &g.Chosen, &g.PatternID, &g.CreatedAt, &g.ResolvedAt)
	if err != nil {
		return g, mapErr(err)
	}
	_ = json.Unmarshal(cands, &g.Candidates)
	return g, nil
}

func (s *Store) CreateSuggestion(ctx context.Context, g *model.Suggestion) error {
	cands, _ := json.Marshal(g.Candidates)
	err := s.pool.QueryRow(ctx,
		`INSERT INTO suggestions (device_id, profile_id, kind, context, input, candidates, model, status, chosen, resolved_at)
		 VALUES (NULLIF($1, ''),$2,$3,$4,$5,$6,$7,$8,$9, CASE WHEN $8 = 'pending' THEN NULL ELSE now() END)
		 RETURNING id::text, created_at`,
		g.DeviceID, g.ProfileID, g.Kind, g.Context, g.Input, cands, g.Model, g.Status, g.Chosen).
		Scan(&g.ID, &g.CreatedAt)
	return mapErr(err)
}

func (s *Store) ResolveSuggestion(ctx context.Context, id, status, chosen string) error {
	return affected(s.pool.Exec(ctx,
		`UPDATE suggestions SET status=$2, chosen=$3, resolved_at=now() WHERE id=$1`, id, status, chosen))
}

func (s *Store) GetSuggestion(ctx context.Context, id string) (model.Suggestion, error) {
	return scanSuggestion(s.pool.QueryRow(ctx, `SELECT `+suggestionCols+` FROM suggestions WHERE id=$1`, id))
}

func (s *Store) ListSuggestions(ctx context.Context, status string, limit int) ([]model.Suggestion, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+suggestionCols+` FROM suggestions WHERE ($1 = '' OR status = $1)
		 ORDER BY created_at DESC LIMIT $2`, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Suggestion{}
	for rows.Next() {
		g, err := scanSuggestion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Store) SetSuggestionPattern(ctx context.Context, id, patternID string) error {
	return affected(s.pool.Exec(ctx, `UPDATE suggestions SET pattern_id=$2 WHERE id=$1`, id, patternID))
}

// ---- utterances ---------------------------------------------------------

func (s *Store) CreateUtterance(ctx context.Context, u *model.Utterance) error {
	err := s.pool.QueryRow(ctx,
		`INSERT INTO utterances (device_id, profile_id, composed, final, corrected, audio_file)
		 VALUES (NULLIF($1, ''),$2,$3,$4,$5,$6) RETURNING id::text, created_at`,
		u.DeviceID, u.ProfileID, u.Composed, u.Final, u.Corrected, u.AudioFile).Scan(&u.ID, &u.CreatedAt)
	return mapErr(err)
}

func (s *Store) ListUtterances(ctx context.Context, limit int) ([]model.Utterance, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id::text, COALESCE(device_id, ''), profile_id::text, composed, final, corrected, audio_file, created_at
		 FROM utterances ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Utterance{}
	for rows.Next() {
		var u model.Utterance
		if err := rows.Scan(&u.ID, &u.DeviceID, &u.ProfileID, &u.Composed, &u.Final, &u.Corrected, &u.AudioFile, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// RecentUtteranceTexts feeds the word predictor with a person's history.
func (s *Store) RecentUtteranceTexts(ctx context.Context, profileID *string, limit int) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT final FROM utterances WHERE profile_id IS NOT DISTINCT FROM $1
		 ORDER BY created_at DESC LIMIT $2`, profileID, limit)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ---- recordings ---------------------------------------------------------

const recordingCols = `id::text, COALESCE(device_id, ''), profile_id::text, label, status, gestures, started_at, ended_at`

func scanRecording(row pgx.Row) (model.Recording, error) {
	var r model.Recording
	var g []byte
	if err := row.Scan(&r.ID, &r.DeviceID, &r.ProfileID, &r.Label, &r.Status, &g, &r.StartedAt, &r.EndedAt); err != nil {
		return r, mapErr(err)
	}
	_ = json.Unmarshal(g, &r.Gestures)
	return r, nil
}

func (s *Store) StartRecording(ctx context.Context, profileID, label string) (model.Recording, error) {
	return scanRecording(s.pool.QueryRow(ctx,
		`INSERT INTO recordings (profile_id, label) VALUES ($1,$2) RETURNING `+recordingCols, profileID, label))
}

func (s *Store) AppendRecordingGesture(ctx context.Context, recordingID, sequence string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE recordings SET gestures = gestures || to_jsonb($2::text) WHERE id=$1 AND status='recording'`,
		recordingID, sequence)
	return mapErr(err)
}

func (s *Store) StopRecording(ctx context.Context, id string) (model.Recording, error) {
	return scanRecording(s.pool.QueryRow(ctx,
		`UPDATE recordings SET status='done', ended_at=now() WHERE id=$1 RETURNING `+recordingCols, id))
}

func (s *Store) GetRecording(ctx context.Context, id string) (model.Recording, error) {
	return scanRecording(s.pool.QueryRow(ctx, `SELECT `+recordingCols+` FROM recordings WHERE id=$1`, id))
}

func (s *Store) ListRecordings(ctx context.Context, limit int) ([]model.Recording, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+recordingCols+` FROM recordings ORDER BY started_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Recording{}
	for rows.Next() {
		r, err := scanRecording(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
