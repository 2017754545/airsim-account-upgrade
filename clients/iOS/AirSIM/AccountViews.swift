import SwiftUI
import UIKit

// MARK: - 账号中枢的两个主页面（参考视频版式）
//
// 「设备」Tab：账号下全部被控端卡片（自动同步，无需配对）+ 设备详情。
// 「账号」Tab：账号概览（已配对设备/正在待机）、偏好设置入口、退出登录。

extension Notification.Name {
    /// 退出登录后 RootView 重新弹出一页式登录 gate。
    static let airsimAccountLoggedOut = Notification.Name("airsim.account.logged-out")
}

/// 账号设备的在线语义：approved = 云端在线，其余离线。
private func onlineLabel(_ status: String) -> (text: String, color: Color) {
    status == "approved" ? ("在线", .green) : ("离线", .orange)
}

// MARK: - 设备 Tab

struct DevicesTabView: View {
    var onCall: (String) -> Void

    @State private var devices: [AccountDevice] = []
    @State private var loading = true
    @State private var loadFailed = false
    @State private var detailDevice: AccountDevice?
    @State private var mintedCode: AccountLoginCode?
    @State private var mintingCode = false
    @State private var message = ""

    var body: some View {
        NavigationStack {
            ZStack {
                Color(uiColor: .systemBackground).ignoresSafeArea()
                ScrollView {
                    VStack(alignment: .leading, spacing: 14) {
                        if loading {
                            HStack { ProgressView(); Text("正在获取账号设备…").font(.footnote) }
                                .padding(.top, 40)
                        } else if loadFailed {
                            VStack(alignment: .leading, spacing: 8) {
                                Text("暂未获取到账号设备").font(.headline)
                                Text("确认此设备已绑定账号，且被控端已用账号 ID + 密码登录。")
                                    .font(.footnote).foregroundColor(.secondary)
                            }
                            .padding(.top, 40)
                        } else if devices.isEmpty {
                            VStack(alignment: .leading, spacing: 8) {
                                Text("账号下暂无被控端").font(.headline)
                                Text("在 Android 被控端打开 AirSIM App，输入账号 ID 与密码登录后，设备会自动出现在这里。")
                                    .font(.footnote).foregroundColor(.secondary)
                            }
                            .padding(.top, 40)
                        } else {
                            ForEach(devices) { device in
                                deviceCard(device)
                            }
                            footnote
                        }
                        loginCodeCard
                    }
                    .padding(.horizontal, 16)
                    .padding(.bottom, 24)
                }
            }
            .navigationTitle("设备")
            .navigationBarTitleDisplayMode(.large)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    NavigationLink {
                        AirSIMSettingsView()
                    } label: {
                        Image(systemName: "plus")
                    }
                    .accessibilityLabel("添加或管理设备")
                }
            }
            .refreshable { await loadDevices() }
            .task { await loadDevices() }
            .sheet(item: $detailDevice) { device in
                DeviceDetailSheet(device: device)
                    .presentationDetents([.medium, .large])
            }
        }
    }

    private func deviceCard(_ device: AccountDevice) -> some View {
        let online = onlineLabel(device.approvalStatus)
        return VStack(alignment: .leading, spacing: 12) {
            HStack(spacing: 12) {
                Image(systemName: device.deviceKind == "android_agent"
                        ? "iphone.gen3" : "iphone.smartphone")
                    .font(.title2)
                    .foregroundColor(.blue)
                    .frame(width: 44, height: 44)
                    .background(Color.blue.opacity(0.12))
                    .cornerRadius(12)
                VStack(alignment: .leading, spacing: 2) {
                    Text(device.displayName).font(.headline)
                    Text(device.model.isEmpty ? device.kindLabel : "\(device.model) · \(device.kindLabel)")
                        .font(.caption).foregroundColor(.secondary)
                }
                Spacer()
                Text(online.text).font(.footnote).foregroundColor(online.color)
            }
            HStack {
                Text("设备详情").font(.subheadline).foregroundColor(.blue)
                Spacer()
                Image(systemName: "chevron.right").font(.caption).foregroundColor(.secondary)
            }
            .contentShape(Rectangle())
            .onTapGesture { detailDevice = device }
        }
        .padding(16)
        .background(Color(uiColor: .secondarySystemBackground))
        .cornerRadius(16)
    }

    /// 新增被控端：生成登录凭据提示（账号 ID + 密码即可登录）。
    private var loginCodeCard: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("添加被控端").font(.headline)
            Text("在被控端 App 输入账号 ID 与注册时设置的密码即可登录；同一账号可登录多台安卓设备。")
                .font(.footnote).foregroundColor(.secondary)
            Button {
                mintCode()
            } label: {
                Text(mintingCode ? "正在生成…"
                     : mintedCode == nil ? "生成备用登录码（可选）" : "重新生成登录码")
                    .frame(maxWidth: .infinity).padding(.vertical, 6)
            }
            .buttonStyle(.bordered)
            .disabled(mintingCode)
            if let minted = mintedCode {
                Text("备用登录码：\(minted.code)")
                    .font(.subheadline.weight(.semibold).monospaced())
                    .textSelection(.enabled)
            }
            if !message.isEmpty {
                Text(message).font(.caption).foregroundColor(.blue)
            }
        }
        .padding(16)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color(uiColor: .secondarySystemBackground))
        .cornerRadius(16)
    }

    private var footnote: some View {
        Text("按号码独立待机：移除待机号码，已保存的短信与通话记录仍会保留。")
            .font(.caption).foregroundColor(.secondary)
    }

    private func loadDevices() async {
        loading = devices.isEmpty
        loadFailed = false
        devices = await AccountCloud.listDevices() ?? []
        if devices.isEmpty && !AccountCloud.isAccountApproved { loadFailed = true }
        loading = false
    }

    private func mintCode() {
        mintingCode = true
        Task { @MainActor in
            defer { mintingCode = false }
            do {
                mintedCode = try await AccountCloud.createLoginCode()
            } catch {
                message = error.localizedDescription
            }
        }
    }
}

// MARK: - 设备详情

private struct DeviceDetailSheet: View {
    let device: AccountDevice

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    infoRow("设备", device.displayName)
                    infoRow("类型", device.kindLabel)
                    infoRow("型号", device.model.isEmpty ? "—" : device.model)
                    infoRow("审批状态", device.approvalStatus)
                    infoRow("注册时间", device.registeredAt.isEmpty ? "—" : device.registeredAt.replacingOccurrences(of: "T", with: " ").prefix(16))
                    infoRow("连接", device.approvalStatus == "approved" ? "云端在线" : "离线")
                }
                .padding(16)
            }
            .navigationTitle("设备详情")
            .navigationBarTitleDisplayMode(.inline)
        }
    }

    private func infoRow(_ title: String, _ value: String) -> some View {
        HStack {
            Text(title).foregroundColor(.secondary)
            Spacer()
            Text(value).fontWeight(.medium)
        }
        .font(.subheadline)
        .padding(.vertical, 10)
        .padding(.horizontal, 14)
        .background(Color(uiColor: .secondarySystemBackground))
        .cornerRadius(12)
    }
}

// MARK: - 账号 Tab

struct AccountTabView: View {
    @State private var devices: [AccountDevice] = []
    @State private var loading = true
    @State private var confirmLogout = false

    private var approvedAgents: Int { devices.filter { $0.deviceKind == "android_agent" && $0.approvalStatus == "approved" }.count }

    var body: some View {
        NavigationStack {
            ZStack {
                Color(uiColor: .systemBackground).ignoresSafeArea()
                ScrollView {
                    VStack(alignment: .leading, spacing: 14) {
                        accountHeader
                        summaryCard
                        preferencesCard
                        cloudCard
                        logoutButton
                    }
                    .padding(.horizontal, 16)
                    .padding(.bottom, 24)
                }
            }
            .navigationTitle("账号")
            .navigationBarTitleDisplayMode(.large)
            .refreshable { await load() }
            .task { await load() }
            .alert("退出登录？", isPresented: $confirmLogout) {
                Button("退出登录", role: .destructive) { logout() }
                Button("取消", role: .cancel) {}
            } message: {
                Text("此设备将解除与账号的绑定；被控端不受影响。")
            }
        }
    }

    private var accountHeader: some View {
        HStack(spacing: 12) {
            Image(systemName: "person.crop.circle.fill")
                .font(.system(size: 44))
                .foregroundColor(.blue)
            VStack(alignment: .leading, spacing: 2) {
                Text(UserDefaults.standard.string(forKey: AccountKeys.selfRegisterAccountID) ?? "AirSIM 账号")
                    .font(.headline)
                Text("已绑定此 iPhone").font(.caption).foregroundColor(.secondary)
            }
            Spacer()
        }
    }

    private var summaryCard: some View {
        VStack(spacing: 0) {
            HStack {
                Text("已配对设备").foregroundColor(.secondary)
                Spacer()
                Text("\(devices.count) 台").fontWeight(.medium)
            }
            .padding(.vertical, 12)
            Divider()
            HStack {
                Text("正在待机").foregroundColor(.secondary)
                Spacer()
                Text("\(approvedAgents) 台被控端").fontWeight(.medium)
            }
            .padding(.vertical, 12)
        }
        .font(.subheadline)
        .padding(.horizontal, 14)
        .background(Color(uiColor: .secondarySystemBackground))
        .cornerRadius(16)
    }

    private var preferencesCard: some View {
        VStack(spacing: 0) {
            NavigationLink {
                AirSIMSettingsView()
            } label: {
                HStack {
                    Text("偏好设置").foregroundColor(.primary)
                    Spacer()
                    Image(systemName: "chevron.right").font(.caption).foregroundColor(.secondary)
                }
                .padding(.vertical, 14)
            }
            Divider().padding(.leading, 14)
            HStack {
                Image(systemName: "phone.badge.waveform").foregroundColor(.blue)
                Text("电话使用系统来电界面").padding(.leading, 6)
                Spacer()
                Image(systemName: "checkmark").foregroundColor(.blue)
            }
            .font(.subheadline)
            .padding(.vertical, 12)
            Divider().padding(.leading, 14)
            HStack {
                Image(systemName: "bubble.left").foregroundColor(.blue)
                Text("短信使用普通通知").padding(.leading, 6)
                Spacer()
                Image(systemName: "checkmark").foregroundColor(.blue)
            }
            .font(.subheadline)
            .padding(.vertical, 12)
        }
        .padding(.horizontal, 14)
        .background(Color(uiColor: .secondarySystemBackground))
        .cornerRadius(16)
    }

    private var cloudCard: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("AirSIM Cloud").font(.subheadline.weight(.semibold))
            Text("账号同步、多号码同时待机、云端通话与短信转发均经 AirSIM Cloud 中继完成。")
                .font(.caption).foregroundColor(.secondary)
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color(uiColor: .secondarySystemBackground))
        .cornerRadius(16)
    }

    private var logoutButton: some View {
        Button(role: .destructive) {
            confirmLogout = true
        } label: {
            Text("退出登录")
                .frame(maxWidth: .infinity)
                .padding(.vertical, 10)
        }
        .buttonStyle(.bordered)
        .tint(.red)
    }

    private func load() async {
        loading = devices.isEmpty
        devices = await AccountCloud.listDevices() ?? []
        loading = false
    }

    private func logout() {
        let defaults = UserDefaults.standard
        for key in [AccountKeys.approvalStatus, AccountKeys.invitationCode,
                    AccountKeys.selfRegisterAccountID, AccountKeys.selfRegisterPassword,
                    AccountKeys.lastRegistrationError, AccountKeys.loginCode] {
            defaults.removeObject(forKey: key)
        }
        NotificationCenter.default.post(name: .airsimAccountLoggedOut, object: nil)
    }
}
