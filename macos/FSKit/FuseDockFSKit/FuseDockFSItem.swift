import FSKit
import Foundation

func fusedockInode(_ identifier: String) -> UInt64 {
    var hash: UInt64 = 14695981039346656037
    for byte in identifier.utf8 {
        hash ^= UInt64(byte)
        hash &*= 1099511628211
    }
    return hash == 0 ? 1 : hash
}

final class FuseDockFSItem: FSItem {
    var object: FuseDockObject
    let inode: UInt64

    init(object: FuseDockObject) {
        self.object = object
        self.inode = fusedockInode(object.id)
        super.init()
    }

    var itemType: FSItem.ItemType {
        object.isDirectory ? .directory : .file
    }

    var identifier: FSItem.Identifier {
        FSItem.Identifier(rawValue: inode) ?? .invalid
    }

    var parentIdentifier: FSItem.Identifier {
        object.parentID.isEmpty ? .invalid : (FSItem.Identifier(rawValue: fusedockInode(object.parentID)) ?? .invalid)
    }
}
