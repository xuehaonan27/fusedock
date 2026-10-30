import Foundation
import FSKit

struct FuseDockObject: Decodable, Sendable {
    let nodeID: UInt64
    let parentNodeID: UInt64
    let id: String
    let parentID: String
    let name: String
    let kind: String
    let size: Int64
    let version: String
    let contentVersion: String
    let modified: Int64

    enum CodingKeys: String, CodingKey {
        case nodeID = "node_id"
        case parentNodeID = "parent_node_id"
        case id
        case parentID = "parent_id"
        case name
        case kind
        case size
        case contentVersion = "content_version"
        case version
        case modified
    }

    var isDirectory: Bool { kind == "directory" }
}

struct FuseDockBridgeError: LocalizedError, Sendable {
    let message: String
    var errorDescription: String? { message }
}

final class FuseDockBridge {
    private let token: OpaquePointer

    init(configPath: String) throws {
        var errorPointer: UnsafeMutablePointer<CChar>?
        let handle = Self.withCString(configPath) { fusedock_open($0, &errorPointer) }
        if let handle {
            self.token = handle
            return
        }
        throw Self.takeError(errorPointer, fallback: "Could not open the FuseDock provider")
    }

    deinit {
        fusedock_close(token)
    }

    var volumeID: UUID {
        guard let value = fusedock_volume_id(token) else { return UUID() }
        defer { fusedock_free(value) }
        return UUID(uuidString: String(cString: value)) ?? UUID()
    }

    func root() throws -> FuseDockObject {
        var errorPointer: UnsafeMutablePointer<CChar>?
        guard let value = fusedock_root(token, &errorPointer) else {
            throw Self.takeError(errorPointer, fallback: "Could not read the provider root")
        }
        return try Self.decode(value)
    }

    func lookup(parentID: String, name: String) throws -> FuseDockObject {
        var errorPointer: UnsafeMutablePointer<CChar>?
        let value = Self.withCString(parentID) { parent in
            Self.withCString(name) { child in
                fusedock_lookup(token, parent, child, &errorPointer)
            }
        }
        guard let value else {
            throw Self.takeError(errorPointer, fallback: "Could not look up \(name)")
        }
        return try Self.decode(value)
    }

    func list(parentID: String) throws -> [FuseDockObject] {
        var errorPointer: UnsafeMutablePointer<CChar>?
        let value = Self.withCString(parentID) { parent in
            fusedock_list(token, parent, &errorPointer)
        }
        guard let value else {
            throw Self.takeError(errorPointer, fallback: "Could not list the provider directory")
        }
        return try Self.decode(value)
    }

    func stat(id: String) throws -> FuseDockObject {
        var errorPointer: UnsafeMutablePointer<CChar>?
        let value = Self.withCString(id) { fusedock_stat(token, $0, &errorPointer) }
        guard let value else {
            throw Self.takeError(errorPointer, fallback: "Could not stat the provider object")
        }
        return try Self.decode(value)
    }

    struct OpenRead: Decodable {
        let handle: UInt64
        let snapshot: FuseDockObject
    }

    func openRead(object: FuseDockObject) throws -> OpenRead {
        var errorPointer: UnsafeMutablePointer<CChar>?
        guard let value = Self.withCString(object.id, { id in Self.withCString(object.contentVersion, { version in fusedock_open_read(token, id, version, object.size, &errorPointer) }) }) else {
            throw Self.takeError(errorPointer, fallback: "Could not open the read snapshot")
        }
        return try Self.decode(value)
    }

    func closeRead(handle: UInt64) { fusedock_close_read(token, handle) }

    func read(handle: UInt64, offset: Int64, length: Int, into buffer: FSMutableFileDataBuffer) throws -> Int {
        var errorPointer: UnsafeMutablePointer<CChar>?
        let result: Int64 = buffer.withUnsafeMutableBytes { bytes in
            fusedock_read(token, handle, offset, bytes.baseAddress, Int64(length), &errorPointer)
        }
        guard result >= 0 else {
            throw Self.takeError(errorPointer, fallback: "Could not read the provider file")
        }
        return Int(result)
    }

    private static func decode<T: Decodable>(_ value: UnsafeMutablePointer<CChar>) throws -> T {
        defer { fusedock_free(value) }
        let data = Data(bytes: value, count: strlen(value))
        do {
            return try JSONDecoder().decode(T.self, from: data)
        } catch {
            throw FuseDockBridgeError(message: "Invalid provider response: \(error.localizedDescription)")
        }
    }

    private static func takeError(_ pointer: UnsafeMutablePointer<CChar>?, fallback: String) -> FuseDockBridgeError {
        guard let pointer else { return FuseDockBridgeError(message: fallback) }
        defer { fusedock_free(pointer) }
        return FuseDockBridgeError(message: String(cString: pointer))
    }

    private static func withCString<T>(_ value: String, _ body: (UnsafeMutablePointer<CChar>) -> T) -> T {
        let pointer = strdup(value)!
        defer { free(pointer) }
        return body(pointer)
    }
}
