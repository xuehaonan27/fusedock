import FSKit
import Foundation

final class FuseDockFSItem: FSItem {
    private var storedObject: FuseDockObject
    var object: FuseDockObject {
        get { readLock.lock(); defer { readLock.unlock() }; return storedObject }
        set { readLock.lock(); defer { readLock.unlock() }; storedObject = newValue }
    }
    let inode: UInt64
    let readLock = NSRecursiveLock()
    var readHandle: UInt64 = 0

    init(object: FuseDockObject) {
        self.storedObject = object
        self.inode = object.nodeID
        super.init()
    }

    var itemType: FSItem.ItemType {
        object.isDirectory ? .directory : .file
    }

    var identifier: FSItem.Identifier {
        FSItem.Identifier(rawValue: inode) ?? .invalid
    }

    var parentIdentifier: FSItem.Identifier {
        object.parentID.isEmpty ? .invalid : (FSItem.Identifier(rawValue: object.parentNodeID) ?? .invalid)
    }
}
