#if FUSEDOCK_FSKIT27
import Foundation
import FSKit
import OSLog

@available(macOS 27.0, *)
final class FuseDockCachedFSVolume: FuseDockFSVolume, FSVolume.DataCacheHandler {
    private var cacheTimer: DispatchSourceTimer?

    override func startCacheMaintenance() {
        let timer = DispatchSource.makeTimerSource(queue: DispatchQueue(label: "dev.fusedock.fskit.expiry", qos: .utility))
        timer.schedule(deadline: .now() + bridge.entryTTL, repeating: bridge.entryTTL)
        timer.setEventHandler { [weak self] in self?.expireDirectoryEntries() }
        cacheTimer = timer
        timer.resume()
        Logger.fusedock.info("FSKit 27 cache lifecycle started")
    }

    override func stopCacheMaintenance() {
        cacheTimer?.cancel()
        cacheTimer = nil
    }

    deinit { cacheTimer?.cancel() }

    private func expireDirectoryEntries() {
        // Expiring directory names makes later opens resolve current metadata.
        // This does not poll providers or discard version-bound file blocks.
        let directories = itemQueue.sync { items.values.filter { $0.object.isDirectory } }
        for directory in directories {
            if let error = setCacheState(for: directory, cacheMode: .readWithCache,
                                        coherencyType: .readCache, action: .invalidate) {
                Logger.fusedock.error("Directory cache expiry failed: \(error.localizedDescription, privacy: .public)")
            }
        }
    }

    override func item(for object: FuseDockObject) throws -> FuseDockFSItem {
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

    func open(
        _ item: FSItem,
        modes: FSVolume.OpenModes,
        cacheMode: FSVolume.DataCacheMode,
        context: FSContext,
        replyHandler reply: @escaping @Sendable (FSOpenItemResult?, (any Error)?) -> Void
    ) {
        guard !modes.contains(.write) else { reply(nil, POSIXError(.EROFS)); return }
        guard let fsItem = item as? FuseDockFSItem else { reply(nil, POSIXError(.EINVAL)); return }
        if fsItem.object.isDirectory {
            reply(FSOpenItemResult(grantedCoherency: .noCache), nil)
            return
        }
        DispatchQueue.global(qos: .userInitiated).async {
            do {
                try self.openRead(fsItem)
                let coherency: FSVolume.KernelCacheCoherencyType = cacheMode == .none ? .noCache : .readCache
                reply(FSOpenItemResult(grantedCoherency: coherency), nil)
            } catch {
                reply(nil, Self.posixError(for: error))
            }
        }
    }

    func upgrade(
        _ item: FSItem,
        cacheMode: FSVolume.DataCacheMode,
        context: FSContext,
        replyHandler reply: @escaping @Sendable (FSUpgradeItemResult?, (any Error)?) -> Void
    ) {
        let coherency: FSVolume.KernelCacheCoherencyType = cacheMode == .none ? .noCache : .readCache
        reply(FSUpgradeItemResult(grantedCoherency: coherency), nil)
    }

    func close(
        _ item: FSItem,
        context: FSContext,
        replyHandler reply: @escaping @Sendable () -> Void
    ) {
        guard let fsItem = item as? FuseDockFSItem else { reply(); return }
        closeRead(fsItem)
        reply()
    }

}
#endif
