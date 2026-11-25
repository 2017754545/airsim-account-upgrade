import Foundation

/// 账号层本地键位与媒体短期票据客户端。
///
/// Relay 端媒体票据有效期 10 分钟；重连或长时间通话时通过
/// `POST /v1/calls/:uuid/media-ticket` 换取新票据。设备身份沿用
/// Keychain 中的 device_secret（iOS 不新增本地密钥存储位置）。
enum AccountKeys {
    static let invitationCode = "airsim.account.invitation-code"
    static let approvalStatus = "airsim.account.approval-status"
}

enum MediaTicketClient {
    struct Response: Decodable {
        let mediaToken: String
        let mediaTicketExpiresAt: String?
        let mediaURL: String
        enum CodingKeys: String, CodingKey {
            case mediaToken = "media_token"
            case mediaTicketExpiresAt = "media_ticket_expires_at"
            case mediaURL = "media_url"
        }
    }

    /// 换取新的媒体票据。失败时返回 nil，调用方继续使用旧凭证（迁移期兼容）。
    static func refresh(
        relayURL: String,
        callUUID: String,
        deviceID: String,
        deviceSecret: String
    ) async -> String? {
        guard let url = RelayEndpoint.url(
            base: relayURL,
            path: "v1/calls/\(callUUID)/media-ticket"
        ) else { return nil }
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.timeoutInterval = 8
        request.cachePolicy = .reloadIgnoringLocalCacheData
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.setValue("Bearer \(deviceSecret)", forHTTPHeaderField: "Authorization")
        request.httpBody = try? JSONEncoder().encode([
            "device_id": deviceID,
            "device_secret": deviceSecret,
        ])
        guard let (data, response) = try? await RelayEgressGate.shared.send(request),
              let http = response as? HTTPURLResponse, http.statusCode == 200,
              let payload = try? JSONDecoder().decode(Response.self, from: data) else {
            return nil
        }
        return payload.mediaToken
    }
}
