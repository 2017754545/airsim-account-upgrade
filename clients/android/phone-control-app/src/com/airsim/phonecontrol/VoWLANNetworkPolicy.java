package com.airsim.phonecontrol;

import java.net.InetAddress;
import java.net.Inet6Address;
import java.util.List;

public final class VoWLANNetworkPolicy {
    private VoWLANNetworkPolicy() {}

    /** User-configured extra private ranges; empty by default. */
    static volatile List<PrivateNetworkPolicy.Prefix> customPrefixes = List.of();

    public static void setCustomPrefixes(List<PrivateNetworkPolicy.Prefix> values) {
        customPrefixes = values == null ? List.of() : List.copyOf(values);
    }

    public static List<PrivateNetworkPolicy.Prefix> customPrefixes() { return customPrefixes; }

    /** 组网功能已移除：不再识别 VPN/overlay 接口，仅保留纯局域网直连。 */
    public static boolean isOverlayInterface(String name, String host) { return false; }

    public static boolean permitsPeer(String remote, String local) {
        int[] r = ipv4(remote);
        int[] l = ipv4(local);
        if (r == null || l == null) return false;
        // App-level HMAC authentication remains mandatory; loopback never makes a peer trusted.
        if (l[0] == 127) return r[0] == 127;
        boolean localPrivate = isPrivateIPv4(local) || isLinkLocal(local)
                || PrivateNetworkPolicy.contains(customPrefixes, local);
        boolean remotePrivate = isPrivateIPv4(remote) || isLinkLocal(remote)
                || PrivateNetworkPolicy.contains(customPrefixes, remote);
        // 仅允许局域网/自定义私有网段互通；公网对端一律拒绝。
        return localPrivate && remotePrivate;
    }

    public static boolean permitsPairingPeer(String remote, String local, String interfaceName) {
        int[] fields = ipv4(local);
        if (fields == null || !permitsPeer(remote, local)) return false;
        return fields[0] == 127 || isVoWLANInterface(interfaceName, local);
    }

    public static boolean permitsPairingPeer(InetAddress remote, InetAddress local, String interfaceName) {
        // Bonjour can connect over IPv6 link-local on a dual-stack Wi-Fi network.
        // This exception is only for the short-lived encrypted pairing listener;
        // it does not open IPv6 control/PCM gateways or overlay/cellular ingress.
        if (remote instanceof Inet6Address && local instanceof Inet6Address) {
            return remote.isLinkLocalAddress() && local.isLinkLocalAddress() && interfaceName != null
                    && (interfaceName.startsWith("wlan") || interfaceName.startsWith("swlan")
                        || interfaceName.startsWith("ap"));
        }
        return remote != null && local != null
                && permitsPairingPeer(remote.getHostAddress(), local.getHostAddress(), interfaceName);
    }

    private static boolean isLinkLocal(String host) {
        int[] fields = ipv4(host);
        return fields != null && fields[0] == 169 && fields[1] == 254;
    }

    private static int[] ipv4(String host) {
        if (host == null) return null;
        String[] fields = host.split("\\.", -1);
        if (fields.length != 4) return null;
        int[] values = new int[4];
        for (int index = 0; index < 4; index++) {
            if (fields[index].isEmpty() || fields[index].length() > 3) return null;
            if (fields[index].length() > 1 && fields[index].charAt(0) == '0') return null;
            for (int c = 0; c < fields[index].length(); c++) {
                if (fields[index].charAt(c) < '0' || fields[index].charAt(c) > '9') return null;
            }
            values[index] = Integer.parseInt(fields[index]);
            if (values[index] > 255) return null;
        }
        return values;
    }

    public static boolean isHotspotInterface(String name, String host) {
        if (name == null || host == null || !(name.startsWith("swlan") || name.startsWith("ap"))) return false;
        if (isCustomPrivate(host)) return true;
        String[] fields = host.split("\\.");
        if (fields.length != 4) return false;
        int first;
        int second;
        try {
            first = Integer.parseInt(fields[0]);
            second = Integer.parseInt(fields[1]);
        } catch (NumberFormatException error) {
            return false;
        }
        if (first == 10 || (first == 172 && second >= 16 && second <= 31)
                || (first == 192 && second == 168)) return true;
        return isCustomPrivate(host);
    }

    public static boolean isVoWLANInterface(String name, String host) {
        if (name == null) return false;
        boolean custom = PrivateNetworkPolicy.contains(customPrefixes, host);
        if (!isPrivateIPv4(host) && !custom) return false;
        return custom || isHotspotInterface(name, host) || name.startsWith("wlan");
    }

    private static boolean isPrivateIPv4(String host) {
        int[] fields = ipv4(host);
        return fields != null && (fields[0] == 10 || (fields[0] == 172 && fields[1] >= 16 && fields[1] <= 31)
                || (fields[0] == 192 && fields[1] == 168));
    }

    static boolean isCustomPrivate(String host) {
        return PrivateNetworkPolicy.contains(customPrefixes, host);
    }

    public static boolean shouldAdvertise(
            boolean hotspotReady, boolean paired, boolean agentReady, boolean pcmReady,
            boolean dialerRoleHeld) {
        return hotspotReady && paired && agentReady && pcmReady && dialerRoleHeld;
    }
}
