package com.airsim.phonecontrol;

import java.net.URI;

/** Relay URL migration for the self-hosted AirSIM endpoint. */
final class RelayURLPolicy {
    static final String INSTALL_RELAY_URL = "https://airsim.bg5ipl.top";
    private static final String LEGACY_HOST = "airsim-push.remotepilot.site";

    private RelayURLPolicy() {}

    static String forInstall(String raw) {
        String value = raw == null ? "" : raw.trim();
        if (value.isEmpty()) return value;
        try {
            URI uri = URI.create(value);
            String path = uri.getRawPath();
            boolean rootPath = path == null || path.isEmpty() || "/".equals(path);
            boolean defaultPort = uri.getPort() == -1 || uri.getPort() == 443;
            if ("https".equalsIgnoreCase(uri.getScheme())
                    && LEGACY_HOST.equalsIgnoreCase(uri.getHost())
                    && uri.getRawUserInfo() == null
                    && defaultPort
                    && rootPath
                    && uri.getRawQuery() == null
                    && uri.getRawFragment() == null) {
                return INSTALL_RELAY_URL;
            }
        } catch (IllegalArgumentException ignored) {
            // Existing validation reports malformed URLs; do not rewrite unknown input.
        }
        return value;
    }
}
