import Foundation
import FSKit

final class FuseDockFSVolume: FSVolume,
                             FSVolume.Operations,
                             FSVolume.ReadWriteOperations,
                             FSVolume.DataCacheHandler {
    let bridge: FuseDockBridge
    let rootItem: FuseDockFSItem
    var items: [String: FuseDockFSItem] = [:]
    let itemQueue = DispatchQueue(label: "dev.fusedock.fskit.items")
    var closed = false

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
        let previous = itemQueue.sync { items[object.id] }
        if let previous {
            previous.readLock.lock()
            let sameRevision = previous.object.contentVersion == object.contentVersion &&
                previous.object.version == object.version && previous.object.size == object.size
            previous.readLock.unlock()
            if sameRevision { return previous }
            // Kernel callbacks can reenter the module during invalidation.
            // The old item keeps its reader; its bytes never change revisions.
            if let error = setCacheState(for: previous, cacheMode: .readWithCache,
                                        coherencyType: .readCache, action: .invalidate) {
                throw error
            }
        }
        let created = FuseDockFSItem(object: object)
        itemQueue.sync { items[object.id] = created }
        return created
    }
}
