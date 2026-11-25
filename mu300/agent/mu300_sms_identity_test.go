package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMU300SMSProducerIdentitySurvivesAutomaticRegistrationAndRecovery(t *testing.T) {
	received := make(chan map[string]any, 8)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/events/sms" {
			t.Error("wrong endpoint")
		}
		var body map[string]any
		if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
			t.Error(e)
		}
		received <- body
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	for _, pairedID := range []string{"mu300-fixture-A", "mu300-fixture-B"} {
		t.Run(pairedID, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "push.json")
			manager := newPushManager(path)
			reg := testPushRegistration(server.URL)
			reg.AndroidAgentID = pairedID
			reg.AndroidAgentKind = "mu300"
			if e := manager.store(reg); e != nil {
				t.Fatal(e)
			}
			reg.AndroidAgentID = ""
			for n := 0; n < 3; n++ {
				if e := manager.store(reg); e != nil {
					t.Fatal(e)
				}
			}
			for _, name := range []string{path, path + ".last-good"} {
				raw, e := os.ReadFile(name)
				if e != nil {
					t.Fatal(e)
				}
				var saved pushRegistration
				if json.Unmarshal(raw, &saved) != nil || saved.AndroidAgentID != pairedID {
					t.Fatal("persisted identity changed")
				}
			}
			if e := os.WriteFile(path, []byte("broken"), 0600); e != nil {
				t.Fatal(e)
			}
			manager = newPushManager(path)
			manager.client = server.Client()
			message := smsMessage{Sender: "fixture-sender", Content: "fixture-body", SIMSlot: "SIM 1", DeliveryID: "same-fixture-delivery", Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
			if sent, e := manager.sendSMS(message); e != nil || !sent {
				t.Fatal("send", sent, e)
			}
			body := <-received
			for key, want := range map[string]string{"agent_id": pairedID, "delivery_id": message.DeliveryID, "content": message.Content, "sender": message.Sender, "sim_slot": message.SIMSlot, "event": "incoming_sms"} {
				if body[key] != want {
					t.Fatalf("wire field %s changed", key)
				}
			}
			if sent, e := manager.sendSMS(message); e != nil || sent {
				t.Fatal("same-manager dedup changed", sent, e)
			}
			select {
			case <-received:
				t.Fatal("duplicate delivery")
			default:
			}
			replacement := reg
			replacement.AndroidAgentID = pairedID + "-replacement"
			if e := manager.store(replacement); e != nil {
				t.Fatal(e)
			}
			manager = newPushManager(path)
			manager.client = server.Client()
			message.DeliveryID = "new-fixture-delivery"
			if sent, e := manager.sendSMS(message); e != nil || !sent {
				t.Fatal("replacement send", sent, e)
			}
			if body := <-received; body["agent_id"] != replacement.AndroidAgentID {
				t.Fatal("explicit identity replacement ignored")
			}
		})
	}
}

func TestSMSRegistrationNeverInventsProducerIdentity(t *testing.T) {
	manager := newPushManager(filepath.Join(t.TempDir(), "push.json"))
	reg := testPushRegistration("https://push.example.com")
	reg.AndroidAgentID = ""
	if e := manager.store(reg); e != nil {
		t.Fatal(e)
	}
	if manager.config.AndroidAgentID != "" {
		t.Fatal("fabricated identity")
	}
	if recovered := newPushManager(manager.configPath); recovered.config.AndroidAgentID != "" {
		t.Fatal("fabricated identity on restart")
	}
}
