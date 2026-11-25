package com.airsim.phonecontrol;

public final class RelayURLPolicyTests {
    public static void main(String[] args) {
        equal("https://airsim.bg5ipl.top", RelayURLPolicy.forInstall("https://airsim-push.remotepilot.site"));
        equal("https://airsim.bg5ipl.top", RelayURLPolicy.forInstall("  https://AIRSIM-PUSH.REMOTEPILOT.SITE/  "));
        equal("https://custom.example", RelayURLPolicy.forInstall("https://custom.example"));
        equal("https://airsim-push.remotepilot.site/path", RelayURLPolicy.forInstall("https://airsim-push.remotepilot.site/path"));
        equal("https://airsim-push.remotepilot.site.evil.test", RelayURLPolicy.forInstall("https://airsim-push.remotepilot.site.evil.test"));
        equal("http://airsim-push.remotepilot.site", RelayURLPolicy.forInstall("http://airsim-push.remotepilot.site"));
        equal("", RelayURLPolicy.forInstall(""));
        System.out.println("RelayURLPolicyTests passed");
    }

    private static void equal(String expected, String actual) {
        if (!expected.equals(actual)) throw new AssertionError("expected=" + expected + " actual=" + actual);
    }
}
