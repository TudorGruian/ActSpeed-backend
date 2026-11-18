package store

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/actspeed/actspeed-backend/internal/model"
)

// ---- memories -----------------------------------------------------------

const memoryCols = `id::text, COALESCE(device_id, ''), profile_id::text, kind, app, content, created_at`

func scanMemory(row pgx.Row) (model.Memory, error) {
	var m model.Memory
	err := row.Scan(&m.ID, &m.DeviceID, &m.ProfileID, &m.Kind, &m.App, &m.Content, &m.CreatedAt)
	return m, mapErr(err)
}

// CreateMemory stores one context entry.
func (s *Store) CreateMemory(ctx context.Context, m *model.Memory) error {
	err := s.pool.QueryRow(ctx,
		`INSERT INTO memories (device_id, profile_id, kind, app, content) VALUES (NULLIF($1, ''),$2,$3,$4,$5)
		 RETURNING id::text, created_at`,
		m.DeviceID, m.ProfileID, m.Kind, m.App, m.Content).Scan(&m.ID, &m.CreatedAt)
	return mapErr(err)
}

// MemoryFilter scopes a memory listing. A profile's memory spans all of its
// devices; without a profile the device's own memory is used.
type MemoryFilter struct {
	DeviceID  string
	ProfileID *string
	Limit     int
}

func (f MemoryFilter) where() (string, []any) {
	switch {
	case f.ProfileID != nil:
		return `profile_id = $1`, []any{*f.ProfileID}
	case f.DeviceID != "":
		return `device_id = $1 AND profile_id IS NULL`, []any{f.DeviceID}
	}
	return `true`, nil
}

// ListMemories returns the newest entries first.
func (s *Store) ListMemories(ctx context.Context, f MemoryFilter) ([]model.Memory, error) {
	where, args := f.where()
	if f.Limit <= 0 {
		f.Limit = 100
	}
	args = append(args, f.Limit)
	rows, err := s.pool.Query(ctx, `SELECT `+memoryCols+` FROM memories WHERE `+where+
		` ORDER BY created_at DESC LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []model.Memory{}
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// HiddenLabels lists proposals the person hid in an app.
func (s *Store) HiddenLabels(ctx context.Context, deviceID string, profileID *string, app string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT substr(content, length($4) + 1) FROM memories
		 WHERE kind = 'note' AND app = $3 AND starts_with(content, $4)
		   AND (device_id = $1 OR ($2::uuid IS NOT NULL AND profile_id = $2::uuid))`,
		deviceID, profileID, app, model.HiddenPrefix)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// DeleteMemory removes one entry.
func (s *Store) DeleteMemory(ctx context.Context, id string) error {
	return affected(s.pool.Exec(ctx, `DELETE FROM memories WHERE id=$1`, id))
}

// ClearMemories removes every entry in scope and reports how many.
func (s *Store) ClearMemories(ctx context.Context, f MemoryFilter) (int64, error) {
	where, args := f.where()
	tag, err := s.pool.Exec(ctx, `DELETE FROM memories WHERE `+where, args...)
	if err != nil {
		return 0, mapErr(err)
	}
	return tag.RowsAffected(), nil
}

// ---- assists ------------------------------------------------------------

const assistCols = `id::text, COALESCE(device_id, ''), profile_id::text, app, summary, proposals, model, status, chosen, width, height, created_at, resolved_at`

func scanAssist(row pgx.Row) (model.Assist, error) {
	var a model.Assist
	var props []byte
	err := row.Scan(&a.ID, &a.DeviceID, &a.ProfileID, &a.App, &a.Summary, &props, &a.Model, &a.Status,
		&a.Chosen, &a.Width, &a.Height, &a.CreatedAt, &a.ResolvedAt)
	if err != nil {
		return a, mapErr(err)
	}
	_ = json.Unmarshal(props, &a.Proposals)
	return a, nil
}

// CreateAssist stores a screen reading.
func (s *Store) CreateAssist(ctx context.Context, a *model.Assist) error {
	props, _ := json.Marshal(a.Proposals)
	err := s.pool.QueryRow(ctx,
		`INSERT INTO assists (device_id, profile_id, app, summary, proposals, model, status, width, height)
		 VALUES (NULLIF($1, ''),$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id::text, created_at`,
		a.DeviceID, a.ProfileID, a.App, a.Summary, props, a.Model, a.Status, a.Width, a.Height).Scan(&a.ID, &a.CreatedAt)
	return mapErr(err)
}

// ResolveAssist records how a reading ended.
func (s *Store) ResolveAssist(ctx context.Context, id, status string, chosen *int) error {
	return affected(s.pool.Exec(ctx,
		`UPDATE assists SET status=$2, chosen=$3, resolved_at=now() WHERE id=$1`, id, status, chosen))
}

// ChosenProposals returns the proposals a person picked in one app, newest
// first: the history that ranks future proposals. A profile's history
// spans its devices.
func (s *Store) ChosenProposals(ctx context.Context, deviceID string, profileID *string, app string, limit int) ([]model.Proposal, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT proposals->chosen FROM assists
		 WHERE status = 'chosen' AND chosen IS NOT NULL AND app = $3
		   AND (device_id = $1 OR ($2::uuid IS NOT NULL AND profile_id = $2::uuid))
		   AND created_at > now() - interval '90 days'
		 ORDER BY created_at DESC LIMIT $4`, deviceID, profileID, app, limit)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []model.Proposal
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var p model.Proposal
		if raw != nil && json.Unmarshal(raw, &p) == nil {
			out = append(out, p)
		}
	}
	return out, rows.Err()
}

// ListAssists returns the newest readings first, optionally for one device
// or one profile.
func (s *Store) ListAssists(ctx context.Context, deviceID, profileID string, limit int) ([]model.Assist, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+assistCols+` FROM assists
		 WHERE ($1 = '' OR device_id = $1) AND ($2 = '' OR profile_id::text = $2)
		 ORDER BY created_at DESC LIMIT $3`, deviceID, profileID, limit)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []model.Assist{}
	for rows.Next() {
		a, err := scanAssist(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// RecentMemories is the session engine's view: the newest entries in a
// person's (or, without a profile, a device's) memory.
func (s *Store) RecentMemories(ctx context.Context, deviceID string, profileID *string, limit int) ([]model.Memory, error) {
	return s.ListMemories(ctx, MemoryFilter{DeviceID: deviceID, ProfileID: profileID, Limit: limit})
}
