#if FUSEDOCK_FSKIT27
import Foundation
import FSKit

@available(macOS 27.0, *)
struct FuseDockRootPublication {
    let from: String
    let to: String
    let item: FuseDockFSItem
    let object: FuseDockObject
    let replaced: FuseDockObject?
    let staged: Bool
    let removed: Bool
}

@available(macOS 27.0, *)
extension FuseDockCachedFSVolume {
    static func sameContent(_ a: FuseDockObject, _ b: FuseDockObject) -> Bool {
        a.id == b.id && a.kind == b.kind && a.size == b.size &&
            a.contentVersion == b.contentVersion && a.version == b.version &&
            a.modified == b.modified && a.created == b.created
    }

    // Metadata is the published namespace; reclaim can release an FSItem
    // without dropping its directory entry or resurrecting an old revision.
    func publishedItem(_ object: FuseDockObject) -> FuseDockFSItem {
        itemQueue.sync {
            if let item = items[object.id], Self.sameContent(item.object, object) { return item }
            let item = FuseDockFSItem(object: object)
            items[object.id] = item
            return item
        }
    }

    func lookupPublishedRoot(_ name: String) -> FuseDockFSItem? {
        publicationLock.lock()
        defer { publicationLock.unlock() }
        if let pending = pendingPublication, pending.staged && pending.from == name { return pending.item }
        return publishedRoot[name].map { publishedItem($0) }
    }

    func rootObjects() -> [FuseDockObject] {
        publicationLock.lock()
        defer { publicationLock.unlock() }
        return publishedRoot.values.sorted { $0.name < $1.name }
    }

    // Runs on the serial maintenance queue. No module lock may be held while
    // the host performs rename: the kernel calls back into this volume.
    func refreshRoot() throws {
        let observed = try bridge.list(parentID: rootItem.object.id)
        let desired = Dictionary(uniqueKeysWithValues: observed.map { ($0.name, $0) })
        let oldNames = Dictionary(grouping: rootObjects(), by: { $0.id })
        for object in observed {
            publicationLock.lock()
            let current = publishedRoot[object.name]
            let moved = oldNames[object.id]?.first {
                $0.name != object.name && Self.sameContent($0, object) &&
                    publishedRoot[$0.name].map { Self.sameContent($0, object) } == true
            }
            if let current, current.id == object.id && current.isDirectory && object.isDirectory {
                publishedRoot[object.name] = object
                publicationLock.unlock()
                continue
            }
            publicationLock.unlock()
            if let current, Self.sameContent(current, object) { continue }
            // A file/directory type change requires removing the former type.
            if let current, current.isDirectory != object.isDirectory {
                try publishRoot(current, from: current.name, removed: true)
            }
            if let moved {
                try publishRoot(object, from: moved.name)
            } else {
                try publishRoot(object)
            }
        }
        for object in rootObjects() where desired[object.name] == nil {
            try publishRoot(object, from: object.name, removed: true)
        }
    }

    private func publishRoot(_ object: FuseDockObject, from: String? = nil, removed: Bool = false) throws {
        publicationLock.lock()
        var temporary: String
        repeat { temporary = ".fusedock-publish-" + UUID().uuidString } while publishedRoot[temporary] != nil
        let source = from ?? temporary
        let destination = removed ? "" : object.name
        let item = from == nil ? FuseDockFSItem(object: object) : publishedItem(publishedRoot[source]!)
        pendingPublication = FuseDockRootPublication(from: source, to: destination, item: item,
            object: object, replaced: publishedRoot[destination], staged: from == nil, removed: removed)
        publicationLock.unlock()
        defer {
            publicationLock.lock()
            pendingPublication = nil
            publicationLock.unlock()
        }
        try bridge.publish(from: source, to: destination, directory: object.isDirectory)
    }

    func commitPublication(item: FSItem, sourceDirectory: FSItem, sourceName: FSFileName,
                           destinationDirectory: FSItem, destinationName: FSFileName,
                           overItem: FSItem?) throws -> FSRenameItemResult {
        publicationLock.lock()
        defer { publicationLock.unlock() }
        guard sourceDirectory === rootItem, destinationDirectory === rootItem,
              let pending = pendingPublication, !pending.removed, sourceName.string == pending.from,
              destinationName.string == pending.to, item === pending.item else { throw POSIXError(.EROFS) }
        if let expected = pending.replaced {
            guard let actual = overItem as? FuseDockFSItem, Self.sameContent(actual.object, expected) else { throw POSIXError(.ESTALE) }
        } else if overItem != nil { throw POSIXError(.ESTALE) }

        let desired = FSRenameItemResult.requestedAttributes
        let itemAttrs = attributes(for: pending.object, desired: desired)
        let rootAttrs = attributes(for: rootItem.object, desired: desired)
        // Native publication needs directory write permission to reach this
        // callback. Authorization is the exact one-use operation above.
        rootAttrs.mode = 0o755
        let overwritten = (overItem as? FuseDockFSItem).map { attributes(for: $0.object, desired: desired) }
        guard let result = FSRenameItemResult(newName: destinationName, renamedItemAttributes: itemAttrs,
            sourceDirectoryAttributes: rootAttrs, destinationDirectoryAttributes: rootAttrs,
            overItemAttributes: overwritten, freeSpace: nil) else { throw POSIXError(.EIO) }
        if !pending.staged { publishedRoot.removeValue(forKey: pending.from) }
        if !pending.removed {
            pending.item.object = pending.object
            publishedRoot[pending.to] = pending.object
            itemQueue.sync { items[pending.object.id] = pending.item }
        }
        pendingPublication = nil
        return result
    }

    func commitRemoval(item: FSItem, directory: FSItem, name: FSFileName) throws -> FSRemoveItemResult {
        publicationLock.lock()
        defer { publicationLock.unlock() }
        guard directory === rootItem, let pending = pendingPublication, pending.removed,
              name.string == pending.from, item === pending.item else { throw POSIXError(.EROFS) }
        let desired = FSRemoveItemResult.requestedAttributes
        let attrs = attributes(for: pending.object, desired: desired)
        attrs.linkCount = 0
        let rootAttrs = attributes(for: rootItem.object, desired: desired)
        rootAttrs.mode = 0o755
        guard let result = FSRemoveItemResult(itemAttributes: attrs, directoryAttributes: rootAttrs, freeSpace: nil) else { throw POSIXError(.EIO) }
        publishedRoot.removeValue(forKey: pending.from)
        pendingPublication = nil
        return result
    }
}
#endif
