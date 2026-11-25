package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func watchCall(a *agent, action, owner string, id watchIdentity) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(id)
	r := httptest.NewRequest("POST", "/internal/watch/"+action, strings.NewReader(string(raw)))
	r.RemoteAddr = "127.0.0.1:5000"
	r.Header.Set(mu300OwnerHeader, owner)
	w := httptest.NewRecorder()
	a.mu300Watch(w, r)
	return w
}
func TestWatchIncomingClaimAndTerminalIdentity(t *testing.T) {
	for _, scenario := range []string{"normal", "other-owner", "iphone-first", "generation", "uncertain", "outgoing", "held"} {
		t.Run(scenario, func(t *testing.T) {
			var mu sync.Mutex
			state := "+CLCC: 1,1,4,0,0\nOK"
			answers := 0
			hangups := 0
			a := testMU300Calls(t, func(cmd string, _ time.Duration) (string, error) {
				mu.Lock()
				defer mu.Unlock()
				switch cmd {
				case "AT+CLCC":
					return state, nil
				case "ATA":
					answers++
					if scenario != "uncertain" {
						state = "+CLCC: 1,1,0,0,0\nOK"
					}
					return "OK", nil
				case "AT+CHUP":
					hangups++
					state = "OK"
					return "OK", nil
				default:
					return "", fmt.Errorf("unexpected mutation %s", cmd)
				}
			})
			a.applyCallPoll(parseCLCC(state), time.Now())
			id := watchIdentity{"fixture-agent", a.calls.Active.ID, resolveTestUUID, 7}
			a.push.mu.Lock()
			a.push.lastCallID, a.push.lastUUID, a.push.lastGeneration = id.CallID, id.CallUUID, id.Generation
			a.push.mu.Unlock()
			owner := "wlan:" + strings.Repeat("a", 64)
			other := "wlan:" + strings.Repeat("b", 64)
			switch scenario {
			case "iphone-first":
				state = "+CLCC: 1,1,0,0,0\nOK"
			case "held":
				state = "+CLCC: 1,1,1,0,0\nOK"
			case "generation":
				id.Generation = 8
			case "outgoing":
				state = "+CLCC: 1,0,2,0,0\nOK"
			}
			w := watchCall(a, "answer", owner, id)
			if scenario == "iphone-first" || scenario == "held" || scenario == "generation" || scenario == "outgoing" {
				if w.Code == 200 || answers != 0 {
					t.Fatal("unsafe answer", w.Code, answers)
				}
				return
			}
			if w.Code != 200 || answers != 1 {
				t.Fatal(w.Code, w.Body.String(), answers)
			}
			w = watchCall(a, "answer", owner, id)
			if w.Code != 200 || answers != 1 {
				t.Fatal("duplicate answer", w.Code, answers)
			}
			if scenario == "uncertain" {
				if !strings.Contains(w.Body.String(), "connecting") {
					t.Fatal("ACK became active", w.Body.String())
				}
				return
			}
			if scenario == "other-owner" {
				if watchCall(a, "answer", other, id).Code == 200 || watchCall(a, "hangup", other, id).Code == 200 || hangups != 0 {
					t.Fatal("owner stolen")
				}
				return
			}
			w = watchCall(a, "hangup", owner, id)
			if w.Code != 200 || hangups != 1 || !strings.Contains(w.Body.String(), "ended") {
				t.Fatal(w.Code, w.Body.String(), hangups)
			}
			// A new call and changed push generation cannot replace the bound native UUID.
			state = "+CLCC: 1,1,4,0,0\nOK"
			a.push.mu.Lock()
			a.push.lastCallID = "next-call"
			a.push.lastGeneration = 8
			a.push.mu.Unlock()
			w = watchCall(a, "status", owner, id)
			if w.Code != 200 || !strings.Contains(w.Body.String(), "ended") {
				t.Fatal("lost terminal tuple", w.Code, w.Body.String())
			}
			if watchCall(a, "answer", owner, id).Code != 200 || answers != 1 {
				t.Fatal("terminal retry caused answer")
			}
		})
	}
}
func TestWatchCheckRejectsStaleOrBusySnapshot(t *testing.T) {
	a := testMU300Calls(t, func(string, time.Duration) (string, error) { return "+CLCC: 1,1,4,0,0\nOK", nil })
	a.applyCallPoll(parseCLCC("+CLCC: 1,1,4,0,0\nOK"), time.Now())
	id := watchIdentity{"fixture", a.calls.Active.ID, resolveTestUUID, 7}
	owner := "wlan:" + strings.Repeat("a", 64)
	a.mu300WatchBindings = map[watchIdentity]*watchBinding{id: {Instance: id.CallID, Owner: owner, Until: time.Now().Add(time.Hour)}}
	w := watchCall(a, "check", owner, id)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "unknown") {
		t.Fatal("stale snapshot", w.Code, w.Body.String())
	}
	a.mu300WatchMu.Lock()
	w = watchCall(a, "check", owner, id)
	a.mu300WatchMu.Unlock()
	if w.Code != 409 {
		t.Fatal("check waited behind modem")
	}
	wrong := id
	wrong.Generation++
	if watchCall(a, "check", owner, wrong).Code != 409 {
		t.Fatal("unbound generation")
	}
}
func TestCallOwnerRejectsMissingGeneration(t *testing.T) {
	p := newPushManager(t.TempDir() + "/push.json")
	p.lastGeneration = 8
	if p.sendCallOwner("call", resolveTestUUID, 0, "watch", "ended") == nil {
		t.Fatal("missing generation borrowed global")
	}
}
