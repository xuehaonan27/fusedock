#if FUSEDOCK_FSKIT27
import Foundation
import FSKit

@available(macOS 27.0, *)
extension FuseDockCachedFSVolume {
    func activateVolume(options: FSTaskOptions,
                        replyHandler reply: @escaping @Sendable (FSActivateResult?, (any Error)?) -> Void) {
        reply(FSActivateResult(rootItem: rootItem), nil)
    }

    func deactivateVolume(options: FSDeactivateOptions,
                          replyHandler reply: @escaping @Sendable ((any Error)?) -> Void) {
        deactivateRoot(options: options, replyHandler: reply)
    }

    func lookupItem(named name: FSFileName, in directory: FSItem, context: FSContext,
                    replyHandler reply: @escaping @Sendable (FSLookupItemResult?, (any Error)?) -> Void) {
        if bridge.canPublishNamespace && directory === rootItem {
            publicationLock.lock()
            defer { publicationLock.unlock() }
            guard let childName = name.string, let item = lookupPublishedRoot(childName) else { reply(nil, POSIXError(.ENOENT)); return }
            let attrs = attributes(for: item.object, desired: FSLookupItemResult.requestedAttributes)
            let result = FSLookupItemResult(foundItem: item, itemName: name, itemAttributes: attrs)
            reply(result, result == nil ? POSIXError(.EIO) : nil)
            return
        }
        resolveEntry(named: name, inDirectory: directory) { item, name, error in
            guard let item = item as? FuseDockFSItem, let name else { reply(nil, error); return }
            item.readLock.lock()
            let attrs = self.attributes(for: item.object, desired: FSLookupItemResult.requestedAttributes)
            item.readLock.unlock()
            let result = FSLookupItemResult(foundItem: item, itemName: name, itemAttributes: attrs)
            reply(result, result == nil ? POSIXError(.EIO) : nil)
        }
    }

    func getAttributes(_ desired: FSItem.GetAttributesRequest, of item: FSItem, context: FSContext,
                       replyHandler reply: @escaping @Sendable (FSGetAttributesResult?, (any Error)?) -> Void) {
        let request = FSItem.GetAttributesRequest()
        request.wantedAttributes = desired.wantedAttributes.union(FSGetAttributesResult.requestedAttributes.wantedAttributes)
        if bridge.canPublishNamespace, let child = item as? FuseDockFSItem,
           child.object.parentID == rootItem.object.id {
            child.readLock.lock()
            defer { child.readLock.unlock() }
            // A remote path may now name a different type. The old vnode keeps
            // its published attributes until native rename/remove replaces it.
            let attrs = attributes(for: child.object, desired: request)
            let result = FSGetAttributesResult(attributes: attrs)
            reply(result, result == nil ? POSIXError(.EIO) : nil)
            return
        }
        readAttributes(request, of: item) { attrs, error in
            guard let attrs else { reply(nil, error); return }
            if self.bridge.canPublishNamespace && item === self.rootItem { attrs.mode = 0o755 }
            let result = FSGetAttributesResult(attributes: attrs)
            reply(result, result == nil ? POSIXError(.EIO) : nil)
        }
    }

    func enumerateDirectory(_ directory: FSItem, startingAt cookie: FSDirectoryCookie,
                            verifier: FSDirectoryVerifier, attributes: FSItem.GetAttributesRequest?,
                            packer: FSDirectoryEntryPacker, context: FSContext,
                            replyHandler reply: @escaping @Sendable (FSEnumerateDirectoryResult?, (any Error)?) -> Void) {
        if bridge.canPublishNamespace && directory === rootItem {
            let objects = rootObjects()
            for (index, object) in objects.enumerated() where index >= Int(cookie.rawValue) {
                let attrs = attributes.map { self.attributes(for: object, desired: $0) }
                if !packer.packEntry(name: FSFileName(string: object.name), itemType: object.isDirectory ? .directory : .file,
                    itemID: FSItem.Identifier(rawValue: object.nodeID)!, nextCookie: FSDirectoryCookie(UInt64(index + 1)), attributes: attrs) { break }
            }
            reply(FSEnumerateDirectoryResult(verifier: 0), nil)
            return
        }
        enumerateEntries(directory, startingAt: cookie, verifier: verifier, attributes: attributes, packer: packer) { verifier, error in
            if let error { reply(nil, error); return }
            reply(FSEnumerateDirectoryResult(verifier: verifier.rawValue), nil)
        }
    }

    func read(from item: FSItem, at offset: off_t, length: Int, into buffer: FSMutableFileDataBuffer,
              replyHandler reply: @escaping @Sendable (FSReadFileResult?, (any Error)?) -> Void) {
        readData(from: item, at: offset, length: length, into: buffer) { (count: Int, error: Error?) in
            if let error { reply(nil, error); return }
            guard let item = item as? FuseDockFSItem else { reply(nil, POSIXError(.EINVAL)); return }
            item.readLock.lock()
            let attrs = self.attributes(for: item.object, desired: FSReadFileResult.requestedAttributes)
            item.readLock.unlock()
            let result = FSReadFileResult(bytesRead: count, itemAttributes: attrs)
            reply(result, result == nil ? POSIXError(.EIO) : nil)
        }
    }

    func createItem(named name: FSFileName, type: FSItem.ItemType, in directory: FSItem,
                    attributes: FSItem.SetAttributesRequest, context: FSContext,
                    replyHandler reply: @escaping @Sendable (FSCreateItemResult?, (any Error)?) -> Void) {
        reply(nil, POSIXError(.EROFS))
    }

    func createLink(to item: FSItem, named name: FSFileName, in directory: FSItem, context: FSContext,
                    replyHandler reply: @escaping @Sendable (FSCreateLinkResult?, (any Error)?) -> Void) {
        reply(nil, POSIXError(.EROFS))
    }

    func createSymbolicLink(named name: FSFileName, in directory: FSItem, attributes: FSItem.SetAttributesRequest,
                           linkContents: FSFileName, context: FSContext,
                           replyHandler reply: @escaping @Sendable (FSCreateSymlinkResult?, (any Error)?) -> Void) {
        reply(nil, POSIXError(.EROFS))
    }

    func removeItem(_ item: FSItem, named name: FSFileName, from directory: FSItem, context: FSContext,
                    replyHandler reply: @escaping @Sendable (FSRemoveItemResult?, (any Error)?) -> Void) {
        guard context.effectiveUserID == Int(getuid()) else { reply(nil, POSIXError(.EACCES)); return }
        do { reply(try commitRemoval(item: item, directory: directory, name: name), nil) }
        catch { reply(nil, error) }
    }

    func renameItem(_ item: FSItem, inDirectory sourceDirectory: FSItem, named sourceName: FSFileName,
                    to destinationName: FSFileName, inDirectory destinationDirectory: FSItem,
                    overItem: FSItem?, context: FSContext,
                    replyHandler reply: @escaping @Sendable (FSRenameItemResult?, (any Error)?) -> Void) {
        guard context.effectiveUserID == Int(getuid()) else { reply(nil, POSIXError(.EACCES)); return }
        do {
            reply(try commitPublication(item: item, sourceDirectory: sourceDirectory, sourceName: sourceName,
                destinationDirectory: destinationDirectory, destinationName: destinationName, overItem: overItem), nil)
        } catch { reply(nil, error) }
    }

    func setAttributes(_ newAttributes: FSItem.SetAttributesRequest, on item: FSItem, context: FSContext,
                       replyHandler reply: @escaping @Sendable (FSSetAttributesResult?, (any Error)?) -> Void) {
        reply(nil, POSIXError(.EROFS))
    }

    func readSymbolicLink(_ item: FSItem, context: FSContext,
                          replyHandler reply: @escaping @Sendable (FSReadSymlinkResult?, (any Error)?) -> Void) {
        reply(nil, POSIXError(.ENOTSUP))
    }

    func write(contents: Data, to item: FSItem, at offset: off_t,
               replyHandler reply: @escaping @Sendable (FSWriteFileResult?, (any Error)?) -> Void) {
        reply(nil, POSIXError(.EROFS))
    }
}
#endif
