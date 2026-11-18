package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/actspeed/actspeed-backend/internal/model"
	"github.com/actspeed/actspeed-backend/internal/session"
	"github.com/actspeed/actspeed-backend/internal/store"
)

const maxScreenshotBytes = 6 << 20

// assist reads a screen sent by a screen client (the Android app) and
// returns proposals bound to gestures.
func (s *Server) assist(w http.ResponseWriter, r *http.Request) {
	id := profileKey(r)
	if s.notPrimary(r) {
		writeErr(w, http.StatusConflict, "only the profile's primary client reads its screen")
		return
	}
	body := struct {
		Image        string             `json:"image"` // base64
		MIME         string             `json:"mime"`
		ImageWidth   int                `json:"image_width"`
		ImageHeight  int                `json:"image_height"`
		ScreenWidth  int                `json:"screen_width"`
		ScreenHeight int                `json:"screen_height"`
		App          string             `json:"app"`
		Title        string             `json:"title"`
		Nodes        []model.ScreenNode `json:"nodes"`
		Goal         string             `json:"goal"`
		Auto         bool               `json:"auto"`
	}{}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxScreenshotBytes*4/3+(2<<20)))
	if err := dec.Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	img, err := base64.StdEncoding.DecodeString(body.Image)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "image must be base64")
		return
	}
	if len(img) == 0 && len(body.Nodes) == 0 {
		writeErr(w, http.StatusBadRequest, "send a screenshot, the screen elements, or both")
		return
	}
	if body.MIME == "" {
		body.MIME = "image/jpeg"
	}
	if body.MIME != "image/jpeg" && body.MIME != "image/png" {
		writeErr(w, http.StatusBadRequest, "mime must be image/jpeg or image/png")
		return
	}
	if body.ScreenWidth <= 0 || body.ScreenHeight <= 0 {
		writeErr(w, http.StatusBadRequest, "screen_width and screen_height are required")
		return
	}
	// Detached from the request so a client that gives up does not lose the reading.
	a, err := s.Sessions.Assist(context.WithoutCancel(r.Context()), id, session.AssistInput{
		Image: img, MIME: body.MIME, ImageWidth: body.ImageWidth, ImageHeight: body.ImageHeight,
		ScreenWidth: body.ScreenWidth, ScreenHeight: body.ScreenHeight,
		App: strings.TrimSpace(body.App), Title: body.Title, Nodes: body.Nodes, Goal: body.Goal, Auto: body.Auto,
	})
	if err != nil {
		if errors.Is(err, session.ErrAIDisabled) {
			writeErr(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		s.Log.Error("screen reading failed", "device", id, "err", err)
		writeErr(w, http.StatusBadGateway, "the screen could not be read")
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) assistError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, session.ErrNoActiveAssist):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, session.ErrUnknownProposal):
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		s.fail(w, err)
	}
}

func (s *Server) chooseProposal(w http.ResponseWriter, r *http.Request) {
	id := profileKey(r)
	body := struct {
		AssistID string `json:"assist_id"`
		Index    int    `json:"index"`
	}{}
	if !decode(w, r, &body) {
		return
	}
	if err := s.Sessions.ChooseProposal(r.Context(), id, body.AssistID, body.Index); err != nil {
		s.assistError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) dismissAssist(w http.ResponseWriter, r *http.Request) {
	id := profileKey(r)
	if err := s.Sessions.DismissAssist(r.Context(), id); err != nil {
		s.assistError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// requestLook asks the device's screen clients to read the screen, e.g.
// from the dashboard.
func (s *Server) requestLook(w http.ResponseWriter, r *http.Request) {
	id := profileKey(r)
	body := struct {
		Goal string `json:"goal"`
	}{}
	if r.ContentLength != 0 && !decode(w, r, &body) {
		return
	}
	if err := s.Sessions.RequestLook(r.Context(), id, strings.TrimSpace(body.Goal)); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) listAssists(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	out, err := s.Store.ListAssists(r.Context(), q.Get("device_id"), q.Get("profile_id"), limit(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- memory -------------------------------------------------------------

func memoryFilter(r *http.Request) store.MemoryFilter {
	f := store.MemoryFilter{DeviceID: r.URL.Query().Get("device_id"), Limit: limit(r)}
	if p := r.URL.Query().Get("profile_id"); p != "" {
		f.ProfileID = &p
	}
	return f
}

// scopeForDevice uses the device's profile memory when it has a profile.
func (s *Server) scopeForDevice(r *http.Request, f store.MemoryFilter) (store.MemoryFilter, error) {
	if f.ProfileID != nil || f.DeviceID == "" {
		return f, nil
	}
	d, err := s.Store.GetDevice(r.Context(), f.DeviceID)
	if err != nil {
		return f, err
	}
	f.ProfileID = d.ProfileID
	return f, nil
}

func (s *Server) listMemories(w http.ResponseWriter, r *http.Request) {
	f, err := s.scopeForDevice(r, memoryFilter(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	out, err := s.Store.ListMemories(r.Context(), f)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createMemory(w http.ResponseWriter, r *http.Request) {
	var m model.Memory
	if !decode(w, r, &m) {
		return
	}
	if m.Content = strings.TrimSpace(m.Content); m.Content == "" {
		writeErr(w, http.StatusBadRequest, "content is required")
		return
	}
	if m.Kind == "" {
		m.Kind = model.MemNote
	}
	switch m.Kind {
	case model.MemNote, model.MemScreen, model.MemAction, model.MemText:
	default:
		writeErr(w, http.StatusBadRequest, "kind must be note, screen, action or text")
		return
	}
	if m.DeviceID != "" {
		d, err := s.Store.GetDevice(r.Context(), m.DeviceID)
		if err != nil {
			s.fail(w, err)
			return
		}
		m.ProfileID = d.ProfileID
	}
	if m.ProfileID == nil {
		writeErr(w, http.StatusBadRequest, "profile_id, or a device_id assigned to a profile, is required")
		return
	}
	if err := s.Store.CreateMemory(r.Context(), &m); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) deleteMemory(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteMemory(r.Context(), chi.URLParam(r, "id")); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// clearMemories forgets a person's (or a device's) whole context.
func (s *Server) clearMemories(w http.ResponseWriter, r *http.Request) {
	f := memoryFilter(r)
	if f.DeviceID == "" && f.ProfileID == nil {
		writeErr(w, http.StatusBadRequest, "device_id or profile_id is required")
		return
	}
	f, err := s.scopeForDevice(r, f)
	if err != nil {
		s.fail(w, err)
		return
	}
	n, err := s.Store.ClearMemories(r.Context(), f)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"deleted": n})
}

// moreProposals asks for further proposals on the screen being read.
func (s *Server) moreProposals(w http.ResponseWriter, r *http.Request) {
	id := profileKey(r)
	a, err := s.Sessions.More(context.WithoutCancel(r.Context()), id)
	if err != nil {
		if errors.Is(err, session.ErrNoActiveAssist) || errors.Is(err, session.ErrAIDisabled) {
			s.assistError(w, err)
			return
		}
		s.Log.Warn("more proposals failed", "device", id, "err", err)
		writeErr(w, http.StatusBadGateway, "the screen could not be read")
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// setField tells the backend that a text field on the device has the
// ActSpeed keyboard open (or closed). While it is open, text mode types
// into the field instead of speaking.
func (s *Server) setField(w http.ResponseWriter, r *http.Request) {
	id := profileKey(r)
	body := struct {
		Active bool   `json:"active"`
		App    string `json:"app"`
		Hint   string `json:"hint"`
	}{}
	if !decode(w, r, &body) {
		return
	}
	if s.notPrimary(r) {
		// A mirroring client's keyboard does not take over the person's typing.
		st, err := s.Sessions.State(r.Context(), id)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, st)
		return
	}
	st, err := s.Sessions.SetField(r.Context(), id, session.Field{Active: body.Active, App: strings.TrimSpace(body.App), Hint: strings.TrimSpace(body.Hint)})
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// hideProposal removes a proposal and stops it being offered in that app.
func (s *Server) hideProposal(w http.ResponseWriter, r *http.Request) {
	id := profileKey(r)
	body := struct {
		AssistID string `json:"assist_id"`
		Index    int    `json:"index"`
	}{}
	if !decode(w, r, &body) {
		return
	}
	a, err := s.Sessions.HideProposal(r.Context(), id, body.AssistID, body.Index)
	if err != nil {
		s.assistError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}
