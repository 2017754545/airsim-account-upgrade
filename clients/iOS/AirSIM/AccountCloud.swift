import Foundation
import SwiftUI

// MARK: - 账号层云服务（iOS 控制端）
//
// iOS 用邀请码注册账号（VoIPPushController.syncRegistration）；注册成功后可以：
//   1. 生成「账号登录码」给任意多台 Android 被控端登录（免配对，自动同步）；
//   2. 通过 /v1/account/devices 枚举同账号的全部设备。

enum AccountKeys {
    static let invitationCode = "airsim.account.invitation-code"
    static let approvalStatus = "airsim.account.approval-status"
    static let loginCode = "airsim.account.login-code"
    static let selfRegisterAccountID = "airsim.account.self-register-account-id"
    static let selfRegisterPassword = "airsim.account.self-register-password"
    static let lastRegistrationError = "airsim.account.last-registration-error"
}

enum AccountCloudError: LocalizedError {
    case invalidRelay
    case http(Int, String)

    var errorDescription: String? {
        switch self {
        case .invalidRelay: return "Relay 地址无效"
        case .http(let status, let message): return message.isEmpty
            ? "服务器错误（HTTP \(status)）" : message
        }
    }
}

struct AccountDevice: Identifiable, Decodable, Equatable {
    let deviceID: String
    let deviceKind: String
    let deviceName: String
    let model: String
    let approvalStatus: String
    let bundleID: String
    let registeredAt: String

    var id: String { deviceID }

    var displayName: String {
        let base = deviceName.isEmpty ? model : deviceName
        let label = deviceKind == "android_agent" ? "安卓被控端" : "iOS 控制端"
        return base.isEmpty ? label : base
    }

    var kindLabel: String {
        deviceKind == "android_agent" ? "安卓被控端" : "iOS 控制端"
    }

    enum CodingKeys: String, CodingKey {
        case deviceID = "device_id"
        case deviceKind = "device_kind"
        case deviceName = "device_name"
        case model
        case approvalStatus = "approval_status"
        case bundleID = "bundle_id"
        case registeredAt = "registered_at"
    }
}

struct AccountLoginCode: Equatable {
    let code: String
    let accountID: String
    let expiresAt: String
}

enum AccountCloud {
    static var isAccountApproved: Bool {
        UserDefaults.standard.string(forKey: AccountKeys.approvalStatus) == "approved"
    }

    /// 账号设备（含同账号的安卓被控端）。返回 nil 表示本设备尚未绑定账号。
    static func listDevices() async -> [AccountDevice]? {
        guard let credentials = await VoIPPushController.shared.cloudCallControlCredentials() else {
            return nil
        }
        guard let url = RelayEndpoint.url(
            base: credentials.relayURL, path: "v1/account/devices") else { return nil }
        var request = URLRequest(url: url)
        request.httpMethod = "GET"
        request.timeoutInterval = 10
        request.cachePolicy = .reloadIgnoringLocalCacheData
        request.setValue("Bearer \(credentials.deviceSecret)", forHTTPHeaderField: "Authorization")
        request.setValue(credentials.deviceID, forHTTPHeaderField: "x-airsim-device-id")
        do {
            let (data, response) = try await RelayEgressGate.shared.send(request)
            guard let http = response as? HTTPURLResponse else { return nil }
            guard http.statusCode == 200 else {
                throw AccountCloudError.http(http.statusCode, errorMessage(from: data))
            }
            struct Payload: Decodable {
                let devices: [AccountDevice]
            }
            return try JSONDecoder().decode(Payload.self, from: data).devices
        } catch {
            return nil
        }
    }

    /// 生成本账号的安卓登录码。明文只出现一次，需立即展示给用户。
    static func createLoginCode() async throws -> AccountLoginCode {
        guard let credentials = await VoIPPushController.shared.cloudCallControlCredentials() else {
            throw AccountCloudError.invalidRelay
        }
        guard let url = RelayEndpoint.url(
            base: credentials.relayURL, path: "v1/account/login-code") else {
            throw AccountCloudError.invalidRelay
        }
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.timeoutInterval = 12
        request.cachePolicy = .reloadIgnoringLocalCacheData
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.setValue("Bearer \(credentials.deviceSecret)", forHTTPHeaderField: "Authorization")
        request.httpBody = try JSONEncoder().encode([
            "device_id": credentials.deviceID,
            "device_secret": credentials.deviceSecret,
        ])
        let (data, response) = try await RelayEgressGate.shared.send(request)
        guard let http = response as? HTTPURLResponse else { throw AccountCloudError.invalidRelay }
        guard http.statusCode == 200 else {
            throw AccountCloudError.http(http.statusCode, errorMessage(from: data))
        }
        struct Payload: Decodable {
            let code: String
            let accountID: String
            let expiresAt: String
            enum CodingKeys: String, CodingKey {
                case code
                case accountID = "account_id"
                case expiresAt = "expires_at"
            }
        }
        let payload = try JSONDecoder().decode(Payload.self, from: data)
        return AccountLoginCode(code: payload.code, accountID: payload.accountID,
                                expiresAt: payload.expiresAt)
    }

    private static func errorMessage(from data: Data) -> String {
        struct Payload: Decodable { let error: String? }
        return (try? JSONDecoder().decode(Payload.self, from: data))?.error ?? ""
    }
}

// MARK: - 账号登录视图（首启 gate）
//
// 未绑定账号时全屏展示：邀请码登录 → 注册响应的 approval_status 决定放行。
// approved 后可直接生成安卓登录码并查看账号设备，也可进入主界面。

struct AccountLoginView: View {
    /// 用户选择暂不绑定 / 已 approved 时回调，进入主界面。
    let onEnterApp: () -> Void

    @State private var invitationCode = UserDefaults.standard.string(forKey: AccountKeys.invitationCode) ?? ""
    @State private var registrationStep = 1
    @State private var accountID = ""
    @State private var accountPassword = ""
    @State private var confirmPassword = ""
    @State private var loginBusy = false
    @State private var message = ""
    @State private var devices: [AccountDevice] = []
    @State private var loadingDevices = false
    @State private var mintedCode: AccountLoginCode?
    @State private var mintingCode = false

    private var approved: Bool { AccountCloud.isAccountApproved }

    var body: some View {
        ZStack {
            // 参考稿为深色沉浸式登录页；登录后内容同样置于深色面板。
            Color(red: 0.04, green: 0.05, blue: 0.08).ignoresSafeArea()
            ScrollView {
                VStack(alignment: .center, spacing: 18) {
                    header
                    statusCard
                    if approved { approvedSection } else { loginSection }
                    skipButton
                }
                .padding(24)
            }
        }
        .preferredColorScheme(.dark)
        .task { await refreshDevices() }
    }

    private var header: some View {
        VStack(spacing: 10) {
            Image(systemName: "cloud.fill")
                .font(.system(size: 56))
                .foregroundColor(Color(red: 0.20, green: 0.55, blue: 1.0))
                .padding(.top, 28)
            Text("AirSIM Cloud")
                .font(.system(size: 32, weight: .bold))
            Text("让每一个号码，都在身边。")
                .font(.subheadline)
                .foregroundColor(Color(white: 0.65))
            VStack(alignment: .leading, spacing: 8) {
                featureLine("person.crop.circle", "登录账号，加载已配对设备")
                featureLine("simcard", "选择设备与号码，同时待机")
                featureLine("iphone.gen3", "在 iPhone 上接打电话、收发短信")
            }
            .padding(.top, 6)
        }
        .frame(maxWidth: .infinity)
    }

    private func featureLine(_ icon: String, _ text: String) -> some View {
        HStack(spacing: 8) {
            Image(systemName: icon)
                .font(.footnote)
                .foregroundColor(Color(white: 0.55))
                .frame(width: 20)
            Text(text)
                .font(.footnote)
                .foregroundColor(Color(white: 0.72))
        }
    }

    private var statusCard: some View {
        VStack(alignment: .leading, spacing: 4) {
            if approved {
                Text("● 账号已绑定")
                    .foregroundColor(.green)
            } else if UserDefaults.standard.string(forKey: AccountKeys.approvalStatus) == "pending" {
                Text("● 已提交注册，等待管理员批准")
                    .foregroundColor(.orange)
            } else {
                Text("● 此设备尚未绑定账号")
                    .foregroundColor(.secondary)
            }
        }
        .font(.footnote)
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(14)
        .background(Color(uiColor: .secondarySystemBackground))
        .cornerRadius(14)
    }

    private var loginSection: some View {
        VStack(alignment: .leading, spacing: 12) {
            if registrationStep == 1 {
                // 第一步：服务器签发的邀请码（换取建号资格）
                Text("邀请码")
                    .font(.footnote)
                    .foregroundColor(.secondary)
                TextField("邀请码（如 ASR-XXXX-XXXX-XXXX）", text: $invitationCode)
                    .textFieldStyle(.roundedBorder)
                    .textInputAutocapitalization(.characters)
                    .autocorrectionDisabled()
                    .accessibilityIdentifier("account.invitationCode")
                Button {
                    message = ""
                    registrationStep = 2
                } label: {
                    Text("下一步").frame(maxWidth: .infinity).padding(.vertical, 6)
                }
                .buttonStyle(.borderedProminent)
                .disabled(invitationCode.trimmingCharacters(in: .whitespaces).isEmpty)
                Text("邀请码由管理员在服务器生成，一次性有效；输入后即可设置你的账号 ID 与密码。")
                    .font(.caption)
                    .foregroundColor(.secondary)
            } else {
                // 第二步：自设账号 ID + 密码（建号并绑定此设备）
                Text("设置账号")
                    .font(.footnote)
                    .foregroundColor(.secondary)
                TextField("账号 ID（字母或数字，如 family）", text: $accountID)
                    .textFieldStyle(.roundedBorder)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                    .accessibilityIdentifier("account.accountID")
                SecureField("密码（至少 8 位）", text: $accountPassword)
                    .textFieldStyle(.roundedBorder)
                    .accessibilityIdentifier("account.password")
                SecureField("确认密码", text: $confirmPassword)
                    .textFieldStyle(.roundedBorder)
                HStack(spacing: 10) {
                    Button {
                        registrationStep = 1
                        message = ""
                    } label: {
                        Text("上一步").frame(maxWidth: .infinity).padding(.vertical, 6)
                    }
                    .buttonStyle(.bordered)
                    Button {
                        registerAndBind()
                    } label: {
                        Text(loginBusy ? "正在注册…" : "注册并绑定")
                            .frame(maxWidth: .infinity).padding(.vertical, 6)
                    }
                    .buttonStyle(.borderedProminent)
                    .disabled(loginBusy || accountID.trimmingCharacters(in: .whitespaces).isEmpty
                              || accountPassword.isEmpty || accountPassword != confirmPassword)
                }
                Text("账号 ID 与密码同时用于 Android 被控端登录；密码仅用于登录验证。")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }
            if !message.isEmpty {
                Text(message)
                    .font(.footnote)
                    .foregroundColor(message.contains("成功") ? .green : .blue)
            }
        }
    }

    private var approvedSection: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("账号设备（自动同步）")
                .font(.headline)
            if loadingDevices {
                HStack { ProgressView(); Text("正在获取…").font(.footnote) }
            } else if devices.isEmpty {
                Text("账号下暂无其他设备。用下面的登录码登录 Android 被控端后，设备会自动出现在这里。")
                    .font(.footnote)
                    .foregroundColor(.secondary)
            } else {
                ForEach(devices) { device in
                    HStack {
                        Image(systemName: device.deviceKind == "android_agent"
                                ? "iphone.gen3" : "iphone.smartphone")
                            .foregroundColor(device.approvalStatus == "approved" ? .green : .orange)
                        VStack(alignment: .leading, spacing: 2) {
                            Text(device.displayName).font(.subheadline)
                            Text("\(device.kindLabel) · \(device.approvalStatus)")
                                .font(.caption).foregroundColor(.secondary)
                        }
                        Spacer()
                    }
                    .padding(12)
                    .background(Color(uiColor: .secondarySystemBackground))
                    .cornerRadius(12)
                }
            }

            Divider().padding(.vertical, 6)

            Text("Android 被控端登录码")
                .font(.headline)
            if let minted = mintedCode {
                VStack(alignment: .leading, spacing: 4) {
                    Text("账号 ID：\(minted.accountID)").font(.subheadline.monospaced())
                    Text("登录码：\(minted.code)")
                        .font(.title3.weight(.bold).monospaced())
                        .textSelection(.enabled)
                    Text("有效期至 \(minted.expiresAt)；可多台安卓设备重复使用。")
                        .font(.caption).foregroundColor(.secondary)
                }
                .padding(12)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(Color(uiColor: .secondarySystemBackground))
                .cornerRadius(12)
            } else {
                Text("Android 被控端打开 App，输入账号 ID「\(accountIDForDisplay())」和注册时设置的密码即可登录加入账号（免配对）。")
                    .font(.footnote).foregroundColor(.secondary)
            }
            Button {
                mintLoginCode()
            } label: {
                Text(mintingCode ? "正在生成…"
                     : mintedCode == nil ? "生成安卓登录码" : "重新生成登录码")
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 6)
            }
            .buttonStyle(.bordered)
            .disabled(mintingCode)

            Button {
                onEnterApp()
            } label: {
                Text("进入 AirSIM").frame(maxWidth: .infinity).padding(.vertical, 6)
            }
            .buttonStyle(.borderedProminent)
        }
    }

    private var skipButton: some View {
        Button("稍后在设置中绑定") { onEnterApp() }
            .font(.footnote)
            .frame(maxWidth: .infinity)
            .padding(.top, 8)
    }

    private func login() {
        let trimmed = invitationCode.trimmingCharacters(in: .whitespacesAndNewlines).uppercased()
        guard !trimmed.isEmpty else { return }
        loginBusy = true
        message = ""
        let defaults = UserDefaults.standard
        defaults.set(trimmed, forKey: AccountKeys.invitationCode)
        defaults.removeObject(forKey: AccountKeys.approvalStatus)
        Task { @MainActor in
            await VoIPPushController.shared.syncRegistration()
            loginBusy = false
            if AccountCloud.isAccountApproved {
                message = "登录成功：设备已绑定账号。"
                // 账号化后不再走旧版局域网配对引导。
                UserDefaults.standard.set(true, forKey: "airsim.first-connection-complete")
                await refreshDevices()
            } else if UserDefaults.standard.string(forKey: AccountKeys.approvalStatus) == "pending" {
                message = "注册已提交：等待管理员批准。"
            } else {
                message = "邀请码无效或已过期，请向管理员索取新的邀请码。"
            }
        }
    }

    /// 两步注册的第二步：邀请码 + 自设账号 ID/密码 建号并绑定。
    private func registerAndBind() {
        let trimmedInvite = invitationCode.trimmingCharacters(in: .whitespacesAndNewlines).uppercased()
        let trimmedID = accountID.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        guard !trimmedInvite.isEmpty, !trimmedID.isEmpty else { return }
        guard accountPassword == confirmPassword, accountPassword.count >= 8 else {
            message = "两次输入的密码不一致或不足 8 位。"
            return
        }
        loginBusy = true
        message = ""
        let defaults = UserDefaults.standard
        defaults.set(trimmedInvite, forKey: AccountKeys.invitationCode)
        defaults.set(trimmedID, forKey: AccountKeys.selfRegisterAccountID)
        defaults.set(accountPassword, forKey: AccountKeys.selfRegisterPassword)
        defaults.removeObject(forKey: AccountKeys.approvalStatus)
        defaults.removeObject(forKey: AccountKeys.lastRegistrationError)
        Task { @MainActor in
            await VoIPPushController.shared.syncRegistration()
            loginBusy = false
            if AccountCloud.isAccountApproved {
                message = "注册成功：账号 \(trimmedID) 已创建并绑定。"
                UserDefaults.standard.set(true, forKey: "airsim.first-connection-complete")
                await refreshDevices()
            } else if let error = defaults.string(forKey: AccountKeys.lastRegistrationError) {
                message = error
                // 账号已存在等情况：回到第一步重新输入邀请码。
                if error.contains("409") || error.contains("already exists") {
                    registrationStep = 1
                }
            } else if defaults.string(forKey: AccountKeys.approvalStatus) == "pending" {
                message = "注册已提交：等待管理员批准。"
            } else {
                message = "注册未完成，请检查网络后重试。"
            }
        }
    }

    private func accountIDForDisplay() -> String {
        UserDefaults.standard.string(forKey: AccountKeys.selfRegisterAccountID) ?? "（注册时设置的账号 ID）"
    }

    private func mintLoginCode() {
        mintingCode = true
        Task { @MainActor in
            defer { mintingCode = false }
            do {
                mintedCode = try await AccountCloud.createLoginCode()
                UserDefaults.standard.set(mintedCode?.code, forKey: AccountKeys.loginCode)
            } catch {
                message = error.localizedDescription
            }
        }
    }

    private func refreshDevices() async {
        guard approved else { return }
        loadingDevices = true
        devices = await AccountCloud.listDevices() ?? []
        loadingDevices = false
    }
}
