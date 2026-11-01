import Foundation
import FSKit

extension FuseDockFSVolume: FSVolume.PathConfOperations {
    var maximumLinkCount: Int { 1 }
    var maximumNameLength: Int { 255 }
    var restrictsOwnershipChanges: Bool { true }
    var truncatesLongNames: Bool { false }
    var maximumFileSize: UInt64 { UInt64.max }
}

extension FuseDockFSVolume {
    var supportedVolumeCapabilities: FSVolume.SupportedCapabilities {
        let capabilities = FSVolume.SupportedCapabilities()
        // The provider contract currently guarantees IDs only for one mounted
        // session. Flipping this on requires a daemon-owned durable mapping
        // from every advertised ID back to a NodeKey after extension restart.
        capabilities.supportsPersistentObjectIDs = false
        capabilities.supports64BitObjectIDs = true
        capabilities.supports2TBFiles = true
        capabilities.doesNotSupportVolumeSizes = true
        capabilities.doesNotSupportImmutableFiles = true
        capabilities.doesNotSupportSettingFilePermissions = true
        capabilities.caseFormat = .sensitive
        return capabilities
    }

    var volumeStatistics: FSStatFSResult {
        let result = FSStatFSResult(fileSystemTypeName: "fusedock")
        result.blockSize = 4096
        result.ioSize = 4 << 20
        return result
    }

    func activate(options: FSTaskOptions, replyHandler: @escaping (FSItem?, (any Error)?) -> Void) {
        replyHandler(rootItem, nil)
    }

    func deactivate(options: FSDeactivateOptions = [], replyHandler: @escaping ((any Error)?) -> Void) {
        stopCacheMaintenance()
        replyHandler(nil)
    }

    func mount(options: FSTaskOptions, replyHandler: @escaping (Error?) -> Void) {
        startCacheMaintenance()
        replyHandler(nil)
    }

    func unmount(replyHandler: @escaping () -> Void) {
        stopCacheMaintenance()
        closed = true
        replyHandler()
    }

    func synchronize(flags: FSSyncFlags, replyHandler: @escaping ((any Error)?) -> Void) {
        replyHandler(nil)
    }

    func getAttributes(
        _ desiredAttributes: FSItem.GetAttributesRequest,
        of item: FSItem,
        replyHandler: @escaping (FSItem.Attributes?, Error?) -> Void
    ) {
        guard let fsItem = item as? FuseDockFSItem else {
            replyHandler(nil, POSIXError(.EINVAL))
            return
        }
        DispatchQueue.global(qos: .userInitiated).async {
            do {
                fsItem.readLock.lock()
                defer { fsItem.readLock.unlock() }
                let object = fsItem.object.isDirectory ? try self.bridge.stat(id: fsItem.object.id) : fsItem.object
                fsItem.object = object
                replyHandler(self.attributes(for: object, desired: desiredAttributes), nil)
            } catch {
                replyHandler(nil, Self.posixError(for: error))
            }
        }
    }

    func setAttributes(
        _ newAttributes: FSItem.SetAttributesRequest,
        on item: FSItem,
        replyHandler: @escaping (FSItem.Attributes?, Error?) -> Void
    ) {
        replyHandler(nil, POSIXError(.EROFS))
    }

    func lookupItem(
        named name: FSFileName,
        inDirectory directory: FSItem,
        replyHandler: @escaping (FSItem?, FSFileName?, Error?) -> Void
    ) {
        guard let parent = directory as? FuseDockFSItem, let childName = name.string else {
            replyHandler(nil, nil, POSIXError(.EINVAL))
            return
        }
        DispatchQueue.global(qos: .userInitiated).async {
            do {
                let object = try self.bridge.lookup(parentID: parent.object.id, name: childName)
                let child = try self.item(for: object)
                replyHandler(child, FSFileName(string: object.name), nil)
            } catch {
                replyHandler(nil, nil, Self.posixError(for: error))
            }
        }
    }

    func reclaimItem(_ item: FSItem, replyHandler: @escaping (Error?) -> Void) {
        guard let fsItem = item as? FuseDockFSItem else {
            replyHandler(POSIXError(.EINVAL))
            return
        }
        closeRead(fsItem)
        itemQueue.sync {
            if fsItem !== rootItem && items[fsItem.object.id] === fsItem {
                items.removeValue(forKey: fsItem.object.id)
            }
        }
        replyHandler(nil)
    }

    func enumerateDirectory(
        _ directory: FSItem,
        startingAt cookie: FSDirectoryCookie,
        verifier: FSDirectoryVerifier,
        attributes: FSItem.GetAttributesRequest?,
        packer: FSDirectoryEntryPacker,
        replyHandler: @escaping (FSDirectoryVerifier, Error?) -> Void
    ) {
        guard let parent = directory as? FuseDockFSItem, parent.object.isDirectory else {
            replyHandler(FSDirectoryVerifier(0), POSIXError(.ENOTDIR))
            return
        }
        DispatchQueue.global(qos: .userInitiated).async {
            do {
                let objects = try self.bridge.list(parentID: parent.object.id)
                let start = min(Int(cookie.rawValue), objects.count)
                for index in start..<objects.count {
                    let child = try self.item(for: objects[index])
                    let attrs = attributes.map { self.attributes(for: child.object, desired: $0) }
                    let packed = packer.packEntry(
                        name: FSFileName(string: objects[index].name),
                        itemType: child.itemType,
                        itemID: child.identifier,
                        nextCookie: FSDirectoryCookie(UInt64(index + 1)),
                        attributes: attrs
                    )
                    if !packed { break }
                }
                replyHandler(FSDirectoryVerifier(0), nil)
            } catch {
                replyHandler(FSDirectoryVerifier(0), Self.posixError(for: error))
            }
        }
    }

    func createItem(
        named name: FSFileName,
        type: FSItem.ItemType,
        inDirectory directory: FSItem,
        attributes: FSItem.SetAttributesRequest,
        replyHandler: @escaping (FSItem?, FSFileName?, Error?) -> Void
    ) {
        replyHandler(nil, nil, POSIXError(.EROFS))
    }

    func createSymbolicLink(
        named name: FSFileName,
        inDirectory directory: FSItem,
        attributes: FSItem.SetAttributesRequest,
        linkContents: FSFileName,
        replyHandler: @escaping (FSItem?, FSFileName?, Error?) -> Void
    ) {
        replyHandler(nil, nil, POSIXError(.EROFS))
    }

    func createLink(
        to item: FSItem,
        named name: FSFileName,
        inDirectory directory: FSItem,
        replyHandler: @escaping (FSFileName?, Error?) -> Void
    ) {
        replyHandler(nil, POSIXError(.EROFS))
    }

    func removeItem(
        _ item: FSItem,
        named name: FSFileName,
        fromDirectory directory: FSItem,
        replyHandler: @escaping (Error?) -> Void
    ) {
        replyHandler(POSIXError(.EROFS))
    }

    func renameItem(
        _ item: FSItem,
        inDirectory sourceDirectory: FSItem,
        named sourceName: FSFileName,
        to destinationName: FSFileName,
        inDirectory destinationDirectory: FSItem,
        overItem: FSItem?,
        replyHandler: @escaping (FSFileName?, Error?) -> Void
    ) {
        replyHandler(nil, POSIXError(.EROFS))
    }

    func readSymbolicLink(
        _ item: FSItem,
        replyHandler: @escaping (FSFileName?, Error?) -> Void
    ) {
        replyHandler(nil, POSIXError(.ENOTSUP))
    }

    func read(
        from item: FSItem,
        at offset: off_t,
        length: Int,
        into buffer: FSMutableFileDataBuffer,
        replyHandler: @escaping (Int, Error?) -> Void
    ) {
        guard let fsItem = item as? FuseDockFSItem, !fsItem.object.isDirectory else {
            replyHandler(0, POSIXError(.EISDIR))
            return
        }
        DispatchQueue.global(qos: .userInitiated).async {
            do {
                fsItem.readLock.lock()
                let handle = fsItem.readHandle
                fsItem.readLock.unlock()
                guard handle != 0 else { replyHandler(0, POSIXError(.EBADF)); return }
                let count = try self.bridge.read(
                    handle: handle,
                    offset: Int64(offset),
                    length: length,
                    into: buffer
                )
                replyHandler(count, nil)
            } catch {
                replyHandler(0, Self.posixError(for: error))
            }
        }
    }

    func write(
        contents: Data,
        to item: FSItem,
        at offset: off_t,
        replyHandler: @escaping (Int, (any Error)?) -> Void
    ) {
        replyHandler(0, POSIXError(.EROFS))
    }

    private func attributes(for object: FuseDockObject, desired: FSItem.GetAttributesRequest) -> FSItem.Attributes {
        let attrs = FSItem.Attributes()
        if desired.isAttributeWanted(.uid) { attrs.uid = getuid() }
        if desired.isAttributeWanted(.gid) { attrs.gid = getgid() }
        if desired.isAttributeWanted(.mode) { attrs.mode = object.isDirectory ? 0o555 : 0o444 }
        if desired.isAttributeWanted(.linkCount) { attrs.linkCount = 1 }
        if desired.isAttributeWanted(.size) { attrs.size = UInt64(max(0, object.size)) }
        if desired.isAttributeWanted(.allocSize) { attrs.allocSize = UInt64(max(0, object.size)) }
        if desired.isAttributeWanted(.fileID) {
            attrs.fileID = FSItem.Identifier(rawValue: object.nodeID) ?? .invalid
        }
        if desired.isAttributeWanted(.parentID) {
            let parent = object.parentID.isEmpty ? 0 : object.parentNodeID
            attrs.parentID = FSItem.Identifier(rawValue: parent) ?? .invalid
        }
        if desired.isAttributeWanted(.type) { attrs.type = object.isDirectory ? .directory : .file }
        if object.modified > 0 {
            let timestamp = timespec(
                tv_sec: Int(object.modified / 1_000_000_000),
                tv_nsec: Int(object.modified % 1_000_000_000)
            )
            if desired.isAttributeWanted(.modifyTime) { attrs.modifyTime = timestamp }
            if desired.isAttributeWanted(.changeTime) { attrs.changeTime = timestamp }
        }
        return attrs
    }

    static func posixError(for error: Error) -> Error {
        if let error = error as? POSIXError { return error }
        let message = error.localizedDescription.lowercased()
        if message.contains("not found") { return POSIXError(.ENOENT) }
        if message.contains("permission") { return POSIXError(.EACCES) }
        if message.contains("changed") { return POSIXError(.ESTALE) }
        if message.contains("unsupported") { return POSIXError(.ENOTSUP) }
        return POSIXError(.EIO, userInfo: [NSLocalizedDescriptionKey: error.localizedDescription])
    }
}
