package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMU300PushNativeUUIDAndLegacyRetryIdentity(t *testing.T) {
	for _, scenario := range []string{"native", "uppercase", "opaque", "legacy-retry", "other-profile"} {
		t.Run(scenario, func(t *testing.T) {
			received := make(chan incomingPushEvent, 4)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var v incomingPushEvent
				if json.NewDecoder(r.Body).Decode(&v) != nil {
					w.WriteHeader(400)
					return
				}
				received <- v
				w.WriteHeader(202)
			}))
			defer server.Close()
			p := newPushManager(filepath.Join(t.TempDir(), "push.json"))
			deliveries := make(chan cloudCallDescriptor, 4)
			p.onCallDelivered = func(d cloudCallDescriptor) { deliveries <- d }
			p.client = server.Client()
			if err := p.store(testPushRegistration(server.URL)); err != nil {
				t.Fatal(err)
			}
			id := "8c2d3456-1234-4abc-9def-0123456789ab"
			profile := "mu300-openwrt"
			if scenario == "uppercase" {
				id = strings.ToUpper(id)
			}
			if scenario == "opaque" {
				id = "legacy-call-17"
			}
			if scenario == "other-profile" {
				profile = "android-avf"
			}
			c := callRecord{ID: id, AndroidID: id, Direction: "incoming", State: "incoming"}
			if scenario == "legacy-retry" {
				p.lastCallID = id
				p.lastUUID = resolveTestUUID
				p.lastSecret = "existing-secret"
				p.lastGeneration = 9
			}
			if sent, err := p.sendIncomingForProfile(c, time.Now(), profile); err != nil || !sent {
				t.Fatal(sent, err)
			}
			got := <-received
			select {
			case d := <-deliveries:
				if d.CallID != got.CallID || d.CallUUID != got.CallUUID || d.Generation != got.Generation || d.CallSecret != got.CallSecret {
					t.Fatal("cloud descriptor differs from push tuple")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("cloud descriptor not delivered")
			}
			if got.CallID != id || !mu300PushUUID.MatchString(got.CallUUID) || got.Generation == 0 {
				t.Fatal("identity lost", got)
			}
			switch scenario {
			case "native", "uppercase":
				if got.CallUUID != strings.ToLower(id) {
					t.Fatal("LAN/push UUID differ")
				}
			case "opaque", "other-profile":
				if got.CallUUID == strings.ToLower(id) {
					t.Fatal("legacy profile or opaque ID coerced")
				}
			case "legacy-retry":
				if got.CallUUID != resolveTestUUID || got.Generation != 9 || got.CallSecret != "existing-secret" {
					t.Fatal("in-flight legacy tuple rewritten")
				}
			}
			// Simulate retry after an unconfirmed delivery, then suppress its duplicate.
			p.mu.Lock()
			p.callDelivered = false
			p.mu.Unlock()
			if _, err := p.sendIncomingForProfile(c, time.Now(), profile); err != nil {
				t.Fatal(err)
			}
			again := <-received
			if again.CallUUID != got.CallUUID || again.Generation != got.Generation || again.CallSecret != got.CallSecret {
				t.Fatal("retry changed tuple")
			}
			if sent, err := p.sendIncomingForProfile(c, time.Now(), profile); err != nil || sent {
				t.Fatal("duplicate delivery")
			}
		})
	}
}

func TestWatchConfirmedTerminalSurvivesFailedPollAndNewCall(t *testing.T) {
	polls := 0
	a := testMU300Calls(t, func(cmd string, _ time.Duration) (string, error) {
		polls++
		return "", errors.New("fixture modem unavailable")
	})
	owner := "wlan:" + strings.Repeat("a", 64)
	id := watchIdentity{"fixture-agent", "native-ended", resolveTestUUID, 7}
	a.mu300WatchBindings = map[watchIdentity]*watchBinding{id: {Instance: id.CallID, Owner: owner, Until: time.Now().Add(time.Hour)}}
	ended := time.Now().Add(-time.Minute)
	a.calls.History = []callRecord{{ID: id.CallID, EndedAt: &ended}}
	a.mu300EarlyPollAt = time.Now().Add(-time.Hour)
	for _, action := range []string{"status", "hangup", "answer", "check", "status"} {
		w := watchCall(a, action, owner, id)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"ended"`) {
			t.Fatal(action, w.Code, w.Body.String())
		}
	}
	if polls != 0 {
		t.Fatal("terminal confirmation queried modem or re-sent mutation", polls)
	}
	// Bounded call history may rotate while the binding remains within its TTL.
	a.calls.History = nil
	a.applyCallPoll(parseCLCC("+CLCC: 1,1,4,0,0\nOK"), time.Now())
	next := a.calls.Active.ID
	w := watchCall(a, "hangup", owner, id)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"ended"`) || a.calls.Active.ID != next || polls != 0 {
		t.Fatal("late hangup touched next call", w.Code, w.Body.String())
	}
	for _, wrong := range []watchIdentity{{id.AgentID, id.CallID, id.CallUUID, 8}, {"other-agent", id.CallID, id.CallUUID, 7}, {id.AgentID, "other-call", id.CallUUID, 7}, {id.AgentID, id.CallID, "491eae43-89f7-4baa-a21e-e15beecbe584", 7}} {
		w = watchCall(a, "check", owner, wrong)
		if w.Code == 200 {
			t.Fatal("terminal leaked across tuple")
		}
	}
	w = watchCall(a, "status", "wlan:"+strings.Repeat("b", 64), id)
	if w.Code == 200 {
		t.Fatal("terminal disclosed to other owner")
	}
	a.mu300WatchBindings[id].Until = time.Now().Add(-time.Second)
	if watchCall(a, "check", owner, id).Code == 200 {
		t.Fatal("expired binding resurrected")
	}
}

func TestWatchMissingInstanceOrStaleLiveCallIsNotTerminal(t *testing.T) {
	a := testMU300Calls(t, func(string, time.Duration) (string, error) { return "", errors.New("fixture timeout") })
	owner := "wlan:" + strings.Repeat("a", 64)
	id := watchIdentity{"fixture-agent", "not-observed", resolveTestUUID, 7}
	a.mu300WatchBindings = map[watchIdentity]*watchBinding{id: {Instance: id.CallID, Owner: owner, Until: time.Now().Add(time.Hour)}}
	now := time.Now()
	a.calls.History = []callRecord{{ID: "different-ended-call", EndedAt: &now}}
	a.mu300EarlyPollAt = now
	w := watchCall(a, "check", owner, id)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"unknown"`) {
		t.Fatal("absence inferred terminal", w.Code, w.Body.String())
	}
	if watchCall(a, "status", owner, id).Code != 409 {
		t.Fatal("failed sample accepted")
	}
	if a.mu300WatchBindings[id].Terminal {
		t.Fatal("unknown latched as ended")
	}
}
