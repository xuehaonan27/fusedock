import Foundation
import FSKit

final class FuseDockFSVolume: FSVolume,
                             FSVolume.Operations,
                             FSVolume.ReadWriteOperations,
                             FSVolume.OpenCloseOperations {
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
            volumeID: FSVolume.Identifier(uuid: UUID()),
            volumeName: FSFileName(string: root.name)
        )
        items[root.id] = rootItem
    }

    func item(for object: FuseDockObject) -> FuseDockFSItem {
        itemQueue.sync {
            if let existing = items[object.id] {
                existing.object = object
                return existing
            }
            let created = FuseDockFSItem(object: object)
            items[object.id] = created
            return created
        }
    }
}
