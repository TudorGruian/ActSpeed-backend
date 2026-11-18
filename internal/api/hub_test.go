package api

import "testing"

func TestHubSendsEachProfileToItsClients(t *testing.T) {
	h := NewHub()
	dashboard := &wsClient{send: make(chan []byte, 4)}
	watcher := &wsClient{profile: "ana", send: make(chan []byte, 4)}
	phone := &wsClient{profile: "ana", device: "android-1", send: make(chan []byte, 4)}
	other := &wsClient{profile: "bob", device: "android-2", send: make(chan []byte, 4)}
	loose := &wsClient{device: "android-3", send: make(chan []byte, 4)} // no profile yet
	for _, c := range []*wsClient{dashboard, watcher, phone, other, loose} {
		h.clients[c] = struct{}{}
	}

	h.Broadcast("ana", []byte("a"))
	h.Broadcast("", []byte("unassigned"))

	want := map[*wsClient]int{dashboard: 2, watcher: 1, phone: 1, other: 0, loose: 0}
	names := map[*wsClient]string{dashboard: "dashboard", watcher: "watcher", phone: "phone", other: "other", loose: "loose"}
	for c, n := range want {
		if got := len(c.send); got != n {
			t.Errorf("%s got %d messages, want %d", names[c], got, n)
		}
	}

	h.Rebind("android-3", "ana")
	if loose.profile != "ana" || len(loose.send) != 1 {
		t.Fatalf("rebound client has profile %q and %d messages, want ana and the assigned notice", loose.profile, len(loose.send))
	}
	h.Broadcast("ana", []byte("b"))
	if len(loose.send) != 2 {
		t.Fatal("rebound client did not get its new profile's messages")
	}
}
