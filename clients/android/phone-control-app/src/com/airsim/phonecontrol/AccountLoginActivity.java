package com.airsim.phonecontrol;

import android.app.Activity;
import android.content.Intent;
import android.graphics.Color;
import android.graphics.Typeface;
import android.graphics.drawable.GradientDrawable;
import android.os.Build;
import android.os.Bundle;
import android.view.Gravity;
import android.view.View;
import android.view.ViewGroup;
import android.widget.Button;
import android.widget.EditText;
import android.widget.LinearLayout;
import android.widget.ScrollView;
import android.widget.TextView;
import android.widget.Toast;

import org.json.JSONObject;

import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.SecureRandom;

/**
 * 账号登录入口（被控端）。
 *
 * Android 不开放注册：iOS 控制端持服务器邀请码注册账号（自设账号 ID + 密码），
 * 被控端在此输入 账号 ID + 密码 即可直接登录加入账号（免配对、自动同步）。
 * 同一账号可登录多台 Android 设备。
 */
public final class AccountLoginActivity extends Activity {

    private StandaloneAgentStore store;
    private EditText accountInput;
    private EditText codeInput;
    private TextView statusView;
    private Button loginButton;
    private boolean loginInFlight;

    @Override protected void onCreate(Bundle state) {
        super.onCreate(state);
        store = new StandaloneAgentStore(this);
        // 已批准的账号设备直接进入主界面；登录页只在未绑定/待批准时出现。
        if (store.configured() && "approved".equals(store.approvalStatus())) {
            startMain();
            return;
        }
        setContentView(buildLoginView());
        renderStatus();
    }

    @Override protected void onResume() {
        super.onResume();
        renderStatus();
    }

    private View buildLoginView() {
        int bg = 0xFF10141A;
        int card = 0xFF181E28;
        int accent = 0xFF3FA796;

        ScrollView scroll = new ScrollView(this);
        scroll.setBackgroundColor(bg);
        scroll.setFillViewport(true);

        LinearLayout root = new LinearLayout(this);
        root.setOrientation(LinearLayout.VERTICAL);
        root.setPadding(dp(24), dp(48), dp(24), dp(32));
        scroll.addView(root, new ViewGroup.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT));

        TextView title = new TextView(this);
        title.setText("AirSIM Cloud");
        title.setTextSize(30);
        title.setTypeface(Typeface.DEFAULT_BOLD);
        title.setTextColor(0xFFFFFFFF);
        root.addView(title);

        TextView subtitle = new TextView(this);
        subtitle.setText("账号登录 · 无需配对\n输入 iOS 端注册时设置的账号 ID 与密码，直接加入账号并自动同步");
        subtitle.setTextSize(14);
        subtitle.setTextColor(0xFF8FA0B3);
        subtitle.setPadding(0, dp(10), 0, dp(20));
        root.addView(subtitle);

        // 状态卡
        LinearLayout statusCard = new LinearLayout(this);
        statusCard.setOrientation(LinearLayout.VERTICAL);
        statusCard.setPadding(dp(16), dp(14), dp(16), dp(14));
        GradientDrawable cardBg = new GradientDrawable();
        cardBg.setColor(card);
        cardBg.setCornerRadius(dp(14));
        statusCard.setBackground(cardBg);
        statusView = new TextView(this);
        statusView.setTextSize(14);
        statusView.setTextColor(0xFFD7E1EA);
        statusCard.addView(statusView);
        root.addView(statusCard);

        root.addView(fieldLabel("账号 ID（iOS 端注册的账号）"));
        accountInput = lineInput(card);
        accountInput.setHint("例如 myname");
        root.addView(accountInput);

        root.addView(fieldLabel("密码（iOS 端注册账号时设置）"));
        codeInput = lineInput(card);
        codeInput.setHint("账号密码");
        codeInput.setTransformationMethod(android.text.method.PasswordTransformationMethod.getInstance());
        root.addView(codeInput);

        loginButton = filledButton("登录并绑定此设备", accent, 0xFF10514A);
        loginButton.setOnClickListener((View v) -> submitLogin());
        root.addView(loginButton, margin(0, 20, 0, 0));

        TextView footnote = new TextView(this);
        footnote.setText("登录后设备立即加入账号：iPhone 端会自动出现此设备，"
                + "通话与短信经 Relay 云端通道同步，无需同一局域网。"
                + "同一账号可登录多台安卓设备，全部自动出现在 iPhone 的账号设备列表中。");
        footnote.setTextSize(12);
        footnote.setTextColor(0xFF6B7A8C);
        footnote.setPadding(0, dp(24), 0, 0);
        root.addView(footnote);
        return scroll;
    }

    private void renderStatus() {
        if (statusView == null) return;
        StringBuilder text = new StringBuilder();
        boolean configured = store.configured();
        String approval = store.approvalStatus();
        if (!configured) {
            text.append("● 此设备尚未绑定账号\n").append("完成登录后自动加入并同步");
        } else if ("approved".equals(approval)) {
            text.append("● 已绑定账号，正在进入…");
        } else if ("pending".equals(approval)) {
            text.append("● 已登录，等待管理员批准");
        } else if ("revoked".equals(approval)) {
            text.append("● 设备已被撤销，请重新登录");
        } else {
            text.append("● 已配对，账号状态未知\n").append("可重新登录刷新账号绑定");
        }
        JSONObject registration = store.registration();
        if (registration != null && !registration.optString("relay_url", "").isEmpty()) {
            text.append("\nRelay：").append(registration.optString("relay_url"));
        }
        statusView.setText(text.toString());
    }

    private void submitLogin() {
        if (loginInFlight) return;
        String accountID = accountInput.getText().toString().trim();
        String password = codeInput.getText().toString();
        if (accountID.isEmpty() || password.isEmpty()) {
            toast("请输入账号 ID 和密码");
            return;
        }
        loginInFlight = true;
        loginButton.setEnabled(false);
        loginButton.setText("正在登录…");
        statusView.setText("● 正在向 Relay 提交登录…");
        // 网络请求放到后台线程；UI 结果回到主线程。
        new Thread(() -> {
            String error = performLogin(accountID, password);
            runOnUiThread(() -> {
                loginInFlight = false;
                loginButton.setEnabled(true);
                loginButton.setText("登录并绑定此设备");
                if (error == null) {
                    renderStatus();
                    toast("登录成功，设备已加入账号");
                    StandaloneAgentService.start(this);
                    startMain();
                } else {
                    statusView.setText("● 登录失败：" + error);
                }
            });
        }, "airsim-account-login").start();
    }

    /** 后台线程执行；返回 null 表示成功。 */
    private String performLogin(String accountID, String password) {
        try {
            JSONObject registration = new JSONObject()
                    .put("relay_url", RelayURLPolicy.forInstall(RelayURLPolicy.INSTALL_RELAY_URL))
                    .put("device_id", newDeviceID())
                    .put("device_secret", newSecret())
                    .put("device_kind", "android_agent")
                    .put("bundle_id", getPackageName())
                    .put("device_name", deviceName())
                    .put("model", Build.MODEL == null ? "" : Build.MODEL)
                    .put("account_id", accountID)
                    .put("account_password", password)
                    .put("local_mode", false)
                    .put("cloud_enabled", true);
            JSONObject payload = new JSONObject()
                    .put("device_id", registration.getString("device_id"))
                    .put("device_secret", registration.getString("device_secret"))
                    .put("device_kind", "android_agent")
                    .put("bundle_id", registration.getString("bundle_id"))
                    .put("device_name", registration.getString("device_name"))
                    .put("model", registration.getString("model"))
                    .put("account_id", accountID)
                    .put("account_password", password);
            StandaloneRelayClient.Response response = new StandaloneRelayClient()
                    .post(registration, "/v1/agent/login", payload, 15_000);
            if (response.status() == 200) {
                store.saveRegistration(registration.toString());
                // 登录响应即已批准；本地记录审批状态，供主界面状态卡展示。
                String approval = "";
                if (response.body() != null && !response.body().isEmpty()) {
                    approval = new JSONObject(response.body()).optString("approval_status", "");
                }
                store.recordApprovalStatus(approval.isEmpty() ? "approved" : approval);
                return null;
            }
            JSONObject body = response.body() == null || response.body().isEmpty()
                    ? new JSONObject() : new JSONObject(response.body());
            return body.optString("error", "HTTP " + response.status());
        } catch (Exception error) {
            return error.getMessage() == null ? error.getClass().getSimpleName() : error.getMessage();
        }
    }

    private static String deviceName() {
        String manufacturer = Build.MANUFACTURER == null ? "" : Build.MANUFACTURER.trim();
        String model = Build.MODEL == null ? "" : Build.MODEL.trim();
        if (manufacturer.equalsIgnoreCase(model)) return model;
        return (manufacturer + " " + model).trim();
    }

    private static String newDeviceID() {
        return "android-agent-" + randomHex(8);
    }

    private static String newSecret() {
        return randomHex(32);
    }

    private static String randomHex(int bytes) {
        byte[] raw = new byte[bytes];
        new SecureRandom().nextBytes(raw);
        StringBuilder builder = new StringBuilder(raw.length * 2);
        for (byte value : raw) {
            builder.append(Character.forDigit((value >> 4) & 0xF, 16));
            builder.append(Character.forDigit(value & 0xF, 16));
        }
        return builder.toString();
    }

    private void startMain() {
        startActivity(new Intent(this, MainActivity.class));
        finish();
    }

    private void toast(String message) {
        Toast.makeText(this, message, Toast.LENGTH_SHORT).show();
    }

    // ── 小工具：编程式 UI 构建（与主界面深色风格一致） ─────────────────────

    private TextView fieldLabel(String text) {
        TextView view = new TextView(this);
        view.setText(text);
        view.setTextSize(13);
        view.setTextColor(0xFF8FA0B3);
        view.setPadding(0, dp(18), 0, dp(6));
        return view;
    }

    private EditText lineInput(int cardColor) {
        EditText input = new EditText(this);
        input.setTextColor(0xFFFFFFFF);
        input.setHintTextColor(0xFF5E7086);
        input.setTextSize(15);
        input.setMaxLines(1);
        input.setSingleLine(true);
        GradientDrawable box = new GradientDrawable();
        box.setColor(cardColor);
        box.setCornerRadius(dp(10));
        input.setBackground(box);
        input.setPadding(dp(14), dp(12), dp(14), dp(12));
        return input;
    }

    private Button filledButton(String text, int textColor, int bgColor) {
        Button button = new Button(this, null, 0);
        button.setText(text);
        button.setTextSize(16);
        button.setTypeface(Typeface.DEFAULT_BOLD);
        button.setTextColor(textColor);
        GradientDrawable bg = new GradientDrawable();
        bg.setColor(bgColor);
        bg.setCornerRadius(dp(12));
        button.setBackground(bg);
        button.setPadding(0, dp(14), 0, dp(14));
        return button;
    }

    private Button ghostButton(String text, int bgColor) {
        Button button = new Button(this, null, 0);
        button.setText(text);
        button.setTextSize(14);
        button.setTextColor(0xFFB6C2CF);
        GradientDrawable bg = new GradientDrawable();
        bg.setColor(bgColor);
        bg.setCornerRadius(dp(12));
        button.setBackground(bg);
        button.setPadding(0, dp(12), 0, dp(12));
        return button;
    }

    private LinearLayout.LayoutParams margin(int left, int top, int right, int bottom) {
        return new LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT);
    }

    private static int dp(int value) {
        return Math.round(value * android.content.res.Resources.getSystem()
                .getDisplayMetrics().density);
    }
}
