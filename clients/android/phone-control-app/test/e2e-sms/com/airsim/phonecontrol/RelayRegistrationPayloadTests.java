package com.airsim.phonecontrol;

import java.util.TreeSet;
import org.json.JSONArray;
import org.json.JSONObject;

public final class RelayRegistrationPayloadTests {
    public static void main(String[] args) throws Exception {
        JSONObject stored = new JSONObject()
                .put("device_id", "iphone-air").put("device_secret", "s".repeat(32))
                .put("voip_token", "aa".repeat(32)).put("alert_token", "bb".repeat(32))
                .put("watch_voip_token", "cc".repeat(32)).put("watch_bundle_id", "com.example.airsim.watchkitapp")
                .put("live_activity_push_to_start_token", "dd".repeat(32))
                .put("live_activity_presentation", "agent_status")
                .put("bundle_id", "com.example.airsim").put("environment", "production")
                .put("media_transport", "legacy_pcm")
                .put("app_media_capabilities", new JSONArray().put("legacy_pcm").put("opus"))
                .put("agent_media_capabilities", new JSONArray().put("legacy_pcm"))
                .put("force_legacy_pcm", false)
                .put("relay_url", "https://relay.example").put("cloud_enabled", true).put("local_mode", false)
                .put("vowlan_secret", "vowlan").put("e2e_key", "key")
                .put("notification_forwarding", new JSONObject().put("webhook", "https://hooks.example/token"))
                .put("android_agent_id", "android-A").put("android_device_name", "Xiaomi 13 Pro")
                .put("android_manufacturer", "Xiaomi").put("android_model", "2210132C")
                .put("android_phone_number", "+8613800000000").put("android_agent_kind", "standalone");
        JSONObject payload = RelayRegistrationPayload.forRelay(stored);
        equal("[agent_media_capabilities, alert_token, app_media_capabilities, bundle_id, device_id, device_secret, "
                + "environment, force_legacy_pcm, live_activity_presentation, live_activity_push_to_start_token, "
                + "media_transport, voip_token, "
                + "watch_bundle_id, watch_voip_token]", new TreeSet<>(payload.keySet()).toString(), "only the Relay's fields");
        for (String key : payload.keySet()) {
            equal(stored.get(key).toString(), payload.get(key).toString(), key + " keeps its value");
        }
        equal(true, stored.has("notification_forwarding"), "the stored registration is not modified");

        JSONObject minimal = RelayRegistrationPayload.forRelay(new JSONObject().put("device_id", "iphone-air")
                .put("alert_token", "bb".repeat(32)).put("relay_url", "https://relay.example"));
        equal("[alert_token, device_id]", new TreeSet<>(minimal.keySet()).toString(), "absent fields stay absent");

        // What a sync covers: a new Relay URL re-registers at once, a field the Relay never reads does not.
        JSONObject moved = new JSONObject(stored.toString()).put("relay_url", "https://other.example");
        if (RelayRegistrationPayload.syncKey(stored).equals(RelayRegistrationPayload.syncKey(moved))) {
            throw new AssertionError("a new Relay URL must change the sync key");
        }
        JSONObject webhook = new JSONObject(stored.toString())
                .put("notification_forwarding", new JSONObject().put("webhook", "https://hooks.example/other"));
        equal(RelayRegistrationPayload.syncKey(stored), RelayRegistrationPayload.syncKey(webhook),
                "a field the Relay never reads does not trigger a re-post");
        equal("https://airsim.bg5ipl.top", RelayURLPolicy.forInstall("https://airsim-push.remotepilot.site"), "legacy Relay migrates to self-hosted HTTPS");
        equal("https://airsim.bg5ipl.top", RelayURLPolicy.forInstall("  https://AIRSIM-PUSH.REMOTEPILOT.SITE/  "), "legacy host matching is case-insensitive");
        equal("https://custom.example", RelayURLPolicy.forInstall("https://custom.example"), "custom host is preserved");
        equal("https://airsim-push.remotepilot.site/path", RelayURLPolicy.forInstall("https://airsim-push.remotepilot.site/path"), "non-root legacy URL is preserved");
        equal("https://airsim-push.remotepilot.site.evil.test", RelayURLPolicy.forInstall("https://airsim-push.remotepilot.site.evil.test"), "lookalike host is preserved");
        equal("http://airsim-push.remotepilot.site", RelayURLPolicy.forInstall("http://airsim-push.remotepilot.site"), "insecure URL is not migrated");
        equal("", RelayURLPolicy.forInstall(""), "empty URL is preserved");
        System.out.println("Relay registration payload and URL migration tests passed: 10 cases");
    }

    private static void equal(Object expected, Object actual, String label) {
        if (!expected.equals(actual)) throw new AssertionError(label + ": expected=" + expected + " actual=" + actual);
    }
}
