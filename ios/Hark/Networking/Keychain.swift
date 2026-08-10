//
//  Keychain.swift
//  Hark
//
//  A minimal keychain wrapper for the one secret the app holds: the session
//  token. Everything else — server URL, device id — is preferences, not
//  secrets, and lives in UserDefaults.
//

import Foundation
import Security

nonisolated enum Keychain {
    private static let service = "dev.abdeen.hark"
    private static let account = "session-token"

    static var sessionToken: String? {
        get {
            var query = baseQuery()
            query[kSecReturnData as String] = true
            query[kSecMatchLimit as String] = kSecMatchLimitOne
            var result: AnyObject?
            let status = SecItemCopyMatching(query as CFDictionary, &result)
            guard status == errSecSuccess, let data = result as? Data else { return nil }
            return String(data: data, encoding: .utf8)
        }
        set {
            if let newValue {
                store(newValue)
            } else {
                SecItemDelete(baseQuery() as CFDictionary)
            }
        }
    }

    private static func store(_ token: String) {
        let data = Data(token.utf8)
        var query = baseQuery()
        let attributes: [String: Any] = [kSecValueData as String: data]
        let status = SecItemUpdate(query as CFDictionary, attributes as CFDictionary)
        if status == errSecItemNotFound {
            query[kSecValueData as String] = data
            query[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlock
            SecItemAdd(query as CFDictionary, nil)
        }
    }

    private static func baseQuery() -> [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
        ]
    }
}
