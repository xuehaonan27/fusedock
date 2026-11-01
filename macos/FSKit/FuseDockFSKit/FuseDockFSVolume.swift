import Foundation
import FSKit

class FuseDockFSVolume: FSVolume {
    let bridge: FuseDockBridge
    let rootItem: FuseDockFSItem
    var items: [String: FuseDockFSItem] = [:]
    let itemQueue = DispatchQueue(label: "dev.fusedock.fskit.items")
    var closed = false

    func startCacheMaintenance() {}
    func stopCacheMaintenance() {}

    init(configPath: String) throws {
        let bridge = try FuseDockBridge(configPath: configPath)
        let root = try bridge.root()
        guard root.isDirectory else {
            throw POSIXError(.ENOTDIR)
        }
        let rootItem = FuseDockFSItem(object: root)
        self.bridge = bridge
        self.rootItem = rootItem
        super.init(
            volumeID: FSVolume.Identifier(uuid: bridge.volumeID),
            volumeName: FSFileName(string: root.name)
        )
        items[root.id] = rootItem
    }

    func item(for object: FuseDockObject) throws -> FuseDockFSItem {
        itemQueue.sync {
            if let existing = items[object.id] { return existing }
            let created = FuseDockFSItem(object: object)
            items[object.id] = created
            return created
        }
    }

    func openRead(_ item: FuseDockFSItem) throws {
        item.readLock.lock()
        defer { item.readLock.unlock() }
        if item.readHandle != 0 { return }
        let opened = try bridge.openRead(object: item.object)
        // Every FSItem owns one observed version for its lifetime. Reopening
        // a provider handle must preserve both that content and its attributes.
        let sameContent = item.object.contentVersion.isEmpty
            ? opened.snapshot.version == item.object.version
            : opened.snapshot.contentVersion == item.object.contentVersion
        if !sameContent || opened.snapshot.size != item.object.size {
            bridge.closeRead(handle: opened.handle)
            throw POSIXError(.ESTALE)
        }
        item.readHandle = opened.handle
    }

    func closeRead(_ item: FuseDockFSItem) {
        item.readLock.lock()
        let handle = item.readHandle
        item.readHandle = 0
        item.readLock.unlock()
        if handle != 0 { bridge.closeRead(handle: handle) }
    }
}
