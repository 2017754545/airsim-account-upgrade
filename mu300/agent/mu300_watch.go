package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

type watchIdentity struct {
	AgentID    string `json:"agent_id"`
	CallID     string `json:"call_id"`
	CallUUID   string `json:"call_uuid"`
	Generation uint64 `json:"generation"`
}
type watchBinding struct {
	Instance, Owner string
	Until           time.Time
	HangupAttempted bool
	Terminal        bool // latched only from confirmed history for this immutable instance
	Notified        string
}

func (a *agent) mu300Watch(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	action := strings.TrimPrefix(r.URL.Path, "/internal/watch/")
	if err != nil || !net.ParseIP(host).IsLoopback() || a.profile.Name != "mu300-openwrt" {
		http.Error(w, "loopback only", 403)
		return
	}
	if r.Method != "POST" || (action != "status" && action != "check" && action != "answer" && action != "hangup") {
		http.NotFound(w, r)
		return
	}
	var id watchIdentity
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	err = dec.Decode(&id)
	var extra any
	owner, oe := mu300RequestOwner(r)
	if err != nil || dec.Decode(&extra) != io.EOF || oe != nil || owner == "local" || !mu300ValidIdentity(id.AgentID) || !mu300ValidIdentity(id.CallID) || !mu300PushUUID.MatchString(id.CallUUID) || id.Generation == 0 || id.Generation > 1<<63-1 {
		http.Error(w, "identity", 400)
		return
	}
	// Media validation reads a bounded-fresh snapshot without contending with
	// the periodic AT poll. Control still serializes with every native mutation.
	if action == "check" {
		if !a.mu300WatchMu.TryLock() {
			http.Error(w, "busy", 409)
			return
		}
	} else {
		a.mu300CallMu.Lock()
		defer a.mu300CallMu.Unlock()
		if r.Context().Err() != nil {
			return
		}
		a.mu300WatchMu.Lock()
	}
	defer a.mu300WatchMu.Unlock()
	if r.Context().Err() != nil {
		return
	}
	now := time.Now()
	if a.mu300WatchBindings == nil {
		a.mu300WatchBindings = map[watchIdentity]*watchBinding{}
	}
	for k, v := range a.mu300WatchBindings {
		if !now.Before(v.Until) {
			delete(a.mu300WatchBindings, k)
		}
	}
	binding := a.mu300WatchBindings[id]
	// Once this exact binding has terminal evidence, a failed later modem poll
	// or another call must not erase it. Never synthesize terminal from absence.
	terminal := binding != nil && (binding.Terminal || a.mu300WatchState(binding.Instance) == "ended")
	if action != "check" && !terminal {
		if a.mu300ControlSnapshot() != nil {
			http.Error(w, "unconfirmed", 409)
			return
		}
	}
	if binding == nil {
		if action == "check" || a.push == nil {
			http.Error(w, "unknown", 409)
			return
		}
		a.push.mu.Lock()
		matches := a.push.lastCallID == id.CallID && a.push.lastUUID == id.CallUUID && a.push.lastGeneration == id.Generation
		a.push.mu.Unlock()
		if !matches || len(a.mu300WatchBindings) >= 64 {
			http.Error(w, "unknown", 409)
			return
		}
		c, _, e := a.mu300Target(id.CallID)
		if e != nil || c.Direction != "incoming" {
			http.Error(w, "unknown", 409)
			return
		}
		for oldID, old := range a.mu300WatchBindings {
			if old.Instance == c.ID && oldID != id {
				http.Error(w, "instance already bound", 409)
				return
			}
		}
		binding = &watchBinding{Instance: c.ID, Until: now.Add(4 * time.Hour)}
		a.mu300WatchBindings[id] = binding
	}
	state := a.mu300WatchState(binding.Instance)
	if terminal {
		state = "ended"
	}
	if action == "answer" {
		if binding.Owner != "" && binding.Owner != owner {
			http.Error(w, "owned", 409)
			return
		}
		if binding.Owner == "" {
			if state != "incoming" {
				http.Error(w, "not ringing", 409)
				return
			}
			// Same call mutex used by iPhone/cloud control: only first ringing answer
			// can acquire ownership. Keep claim even if the command outcome is unknown.
			binding.Owner = owner
			_ = a.mu300ControlLocked("answer", binding.Instance, "")
			state = a.mu300WatchState(binding.Instance)
		}
	}
	if action == "hangup" {
		if binding.Owner != owner {
			http.Error(w, "not owner", 403)
			return
		}
		if !binding.HangupAttempted && state != "ended" && state != "unknown" {
			binding.HangupAttempted = true
			_ = a.mu300ControlLocked("hangup", binding.Instance, "")
			state = a.mu300WatchState(binding.Instance)
		}
	}
	if binding.Owner != "" && binding.Owner != owner {
		http.Error(w, "owned", 403)
		return
	}
	if state == "ended" {
		binding.Terminal = true
	}
	if binding.Owner == owner && state == "incoming" {
		state = "connecting"
	}
	if binding.Owner == owner && (state == "active" || state == "ended") && binding.Notified != state && a.push != nil {
		binding.Notified = state
		go func(identity watchIdentity, phase string) {
			_ = a.push.sendCallOwner(identity.CallID, identity.CallUUID, identity.Generation, "watch", phase)
		}(id, state)
	}
	writeJSON(w, 200, map[string]any{"state": state, "instance": binding.Instance, "owned": binding.Owner == owner})
}
func (a *agent) mu300WatchState(instance string) string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, c := range a.mu300Calls {
		if c.ID != instance {
			continue
		}
		age := time.Since(a.mu300EarlyPollAt)
		if a.mu300EarlyPollAt.IsZero() || age < 0 || age > 2500*time.Millisecond {
			return "unknown"
		}
		if c.Direction != "incoming" || !a.mu300GoneSince[c.ID].IsZero() {
			return "unknown"
		}
		switch c.State {
		case "incoming", "waiting":
			return "incoming"
		case "active", "held":
			return c.State
		}
		return "unknown"
	}
	// Only a previously verified immutable instance with confirmed terminal
	// history is ended. An unrelated active call or failed lookup is not proof.
	for _, c := range a.calls.History {
		if c.ID == instance && c.EndedAt != nil {
			return "ended"
		}
	}
	return "unknown"
}
