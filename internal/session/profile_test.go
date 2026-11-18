package session

import (
	"context"
	"testing"

	"github.com/actspeed/actspeed-backend/internal/model"
	"github.com/actspeed/actspeed-backend/internal/signal"
)

func TestJoysticksOfOneProfileShareItsSession(t *testing.T) {
	r := newRig(t, model.ProfileSettings{}, nil)
	ctx := context.Background()
	r.play("N |")
	r.m.HandleSample(ctx, "second-joystick", signal.Sample{X: 0, Y: 900})
	r.sample(0, 0, 0, 800)

	if n := len(r.m.sessions); n != 1 {
		t.Fatalf("%d sessions, want one for the profile", n)
	}
	r.out.mu.Lock()
	defer r.out.mu.Unlock()
	if len(r.out.msgs) == 0 {
		t.Fatal("nothing was published")
	}
	for _, m := range r.out.msgs {
		if m.profile != "p1" || m.client != "phone" {
			t.Fatalf("%s went to profile %q client %q, want p1 and the primary client", m.typ, m.profile, m.client)
		}
	}
}

func TestUnassignedJoystickOnlyTellsDashboards(t *testing.T) {
	r := newRig(t, model.ProfileSettings{}, nil)
	r.store.loose = map[string]bool{"stray": true}
	ctx := context.Background()
	r.m.HandleSample(ctx, "stray", signal.Sample{X: 900})
	r.m.HandleSample(ctx, "stray", signal.Sample{X: 900}) // too soon for a second notice

	if n := len(r.m.sessions); n != 0 {
		t.Fatalf("%d sessions, want none", n)
	}
	got := r.out.of("unassigned_input")
	if len(got) != 1 || got[0].(map[string]any)["device_id"] != "stray" {
		t.Fatalf("notices = %v, want one for stray", got)
	}
	if r.out.msgs[0].profile != "" {
		t.Fatalf("notice went to profile %q, want dashboards only", r.out.msgs[0].profile)
	}
}

func TestModeIsStoredOnTheProfile(t *testing.T) {
	r := newRig(t, model.ProfileSettings{}, nil)
	st, err := r.m.SetMode(context.Background(), "p1", model.ModeText, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode != model.ModeText || r.store.profile.Mode != model.ModeText {
		t.Fatalf("state %s, stored %s; want text for both", st.Mode, r.store.profile.Mode)
	}
	if st.PrimaryClient != "phone" {
		t.Fatalf("primary client %q, want phone", st.PrimaryClient)
	}
}

func TestLanguageChangeAppliesAtOnce(t *testing.T) {
	r := newRig(t, model.ProfileSettings{}, nil)
	ctx := context.Background()
	if _, err := r.m.SetMode(ctx, "p1", model.ModeText, nil); err != nil {
		t.Fatal(err)
	}
	r.store.profile.Language = "ro"
	r.m.ProfileChanged(ctx, "p1")

	got := r.out.of("profile_changed")
	if len(got) != 1 || got[0].(map[string]any)["language"] != "ro" {
		t.Fatalf("profile_changed = %v, want one with ro", got)
	}
	st := r.state()
	if st.Language != "ro" {
		t.Fatalf("state language %q, want ro", st.Language)
	}
	if st.Suggest == nil || len(st.Suggest.Phrases) == 0 || st.Suggest.Phrases[0].Text != "Da" {
		t.Fatalf("phrases %+v, want the Romanian ones without new input", st.Suggest)
	}
	if n := len(r.out.of("text_update")); n == 0 {
		t.Fatal("the new phrases were not published")
	}
}

func TestRomanianAlert(t *testing.T) {
	r := newRig(t, model.ProfileSettings{}, nil)
	r.state() // opens the session
	r.store.profile.Language = "ro"
	r.m.ProfileChanged(context.Background(), "p1")
	s := r.m.sessions["p1"]
	s.mu.Lock()
	s.raiseAlert()
	s.mu.Unlock()
	r.m.Wait()
	speak := r.out.of("speak")
	if len(speak) == 0 {
		t.Fatal("nothing spoken")
	}
	d := speak[len(speak)-1].(map[string]any)
	if d["text"] != "Am nevoie de ajutor, vă rog să veniți." || d["language"] != "ro" {
		t.Fatalf("spoke %v, want the Romanian alert in ro", d)
	}
}
