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
        deactivate(options: options, replyHandler: reply)
    }

    func lookupItem(named name: FSFileName, in directory: FSItem, context: FSContext,
                    replyHandler reply: @escaping @Sendable (FSLookupItemResult?, (any Error)?) -> Void) {
        lookupItem(named: name, inDirectory: directory) { item, name, error in
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
        getAttributes(desired, of: item) { attrs, error in
            guard let attrs else { reply(nil, error); return }
            let result = FSGetAttributesResult(attributes: attrs)
            reply(result, result == nil ? POSIXError(.EIO) : nil)
        }
    }

    func enumerateDirectory(_ directory: FSItem, startingAt cookie: FSDirectoryCookie,
                            verifier: FSDirectoryVerifier, attributes: FSItem.GetAttributesRequest?,
                            packer: FSDirectoryEntryPacker, context: FSContext,
                            replyHandler reply: @escaping @Sendable (FSEnumerateDirectoryResult?, (any Error)?) -> Void) {
        enumerateDirectory(directory, startingAt: cookie, verifier: verifier, attributes: attributes, packer: packer) { verifier, error in
            if let error { reply(nil, error); return }
            reply(FSEnumerateDirectoryResult(verifier: verifier.rawValue), nil)
        }
    }

    func read(from item: FSItem, at offset: off_t, length: Int, into buffer: FSMutableFileDataBuffer,
              replyHandler reply: @escaping @Sendable (FSReadFileResult?, (any Error)?) -> Void) {
        read(from: item, at: offset, length: length, into: buffer) { (count: Int, error: Error?) in
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
        reply(nil, POSIXError(.EROFS))
    }

    func renameItem(_ item: FSItem, inDirectory sourceDirectory: FSItem, named sourceName: FSFileName,
                    to destinationName: FSFileName, inDirectory destinationDirectory: FSItem,
                    overItem: FSItem?, context: FSContext,
                    replyHandler reply: @escaping @Sendable (FSRenameItemResult?, (any Error)?) -> Void) {
        reply(nil, POSIXError(.EROFS))
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
