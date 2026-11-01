// Standalone FSKit directory-cache reproduction. No broker or provider code.
import Foundation
import FSKit
import OSLog

@available(macOS 27.0, *)
class RootCacheProbe: FSVolume, FSVolume.Operations, FSVolume.DataCacheHandler {
    let root: FSItem
    let child: FSItem
    let rootFirst: Bool
    let rootNoCache: Bool
    let rootID: UInt64
    let parentID: UInt64
    let probeID: String
    var timer: DispatchSourceTimer?
    let logger = Logger(subsystem: "dev.fusedock.root-cache-probe", category: "probe")

    init(configPath: String) throws {
        let resource = URL(fileURLWithPath: configPath)
        probeID = resource.lastPathComponent
        let source = try resource.resourceValues(forKeys: [.isDirectoryKey]).isDirectory == true ? resource.appendingPathComponent("config") : resource
        let config = try String(contentsOf: source, encoding: .utf8).trimmingCharacters(in: .whitespacesAndNewlines)
        rootFirst = config != "child-first"
        rootNoCache = config == "root-no-cache"
        rootID = config == "root-id-42" ? 42 : 2
        parentID = config == "root-parent-self" ? rootID : 1
        if rootFirst { root = FSItem(); child = FSItem() }
        else { child = FSItem(); root = FSItem() }
        super.init(volumeID: FSVolume.Identifier(uuid: UUID()), volumeName: FSFileName(string: "RootCacheProbe"))
    }
    var supportedVolumeCapabilities: FSVolume.SupportedCapabilities {
        let c = FSVolume.SupportedCapabilities()
        c.supportsPersistentObjectIDs = false
        c.supports64BitObjectIDs = true
        c.doesNotSupportVolumeSizes = true
        c.caseFormat = .sensitive
        return c
    }
    var volumeStatistics: FSStatFSResult { FSStatFSResult(fileSystemTypeName: "fusedock") }
    var maximumLinkCount: Int { 1 }
    var maximumNameLength: Int { 255 }
    var restrictsOwnershipChanges: Bool { true }
    var truncatesLongNames: Bool { false }
    var maximumFileSize: UInt64 { UInt64.max }

    func activate(options: FSTaskOptions, replyHandler: @escaping (FSItem?, Error?) -> Void) { replyHandler(root, nil) }
    func deactivate(options: FSDeactivateOptions, replyHandler: @escaping (Error?) -> Void) { timer?.cancel(); replyHandler(nil) }
    func mount(options: FSTaskOptions, replyHandler: @escaping (Error?) -> Void) {
        let timer = DispatchSource.makeTimerSource(queue: DispatchQueue(label: "root-cache-probe"))
        timer.schedule(deadline: .now() + 1, repeating: 1)
        timer.setEventHandler { [weak self] in self?.probe() }
        self.timer = timer
        timer.resume()
        replyHandler(nil)
    }
    func probe() {
        for (name, item) in [("root",root), ("child",child)] {
            for action: FSVolume.KernelCacheCoherencyAction in [.push, .pushInvalidate, .invalidate, .update] {
                let error = setCacheState(for: item, cacheMode: .readWithCache, coherencyType: self.rootNoCache && item === root ? .noCache : .readCache, action: action) as NSError?
                logger.notice("probe=\(self.probeID, privacy: .public) rootID=\(self.rootID) parentID=\(self.parentID) rootFirst=\(self.rootFirst) rootNoCache=\(self.rootNoCache) item=\(name, privacy: .public) action=\(action.rawValue) domain=\(error?.domain ?? "none", privacy: .public) code=\(error?.code ?? 0)")
            }
        }
    }
    func unmount(replyHandler: @escaping () -> Void) { timer?.cancel(); replyHandler() }
    func synchronize(flags: FSSyncFlags, replyHandler: @escaping (Error?) -> Void) { replyHandler(nil) }
    func reclaimItem(_ item: FSItem, replyHandler: @escaping (Error?) -> Void) { replyHandler(nil) }
    func attrs(_ item: FSItem) -> FSItem.Attributes {
        let a = FSItem.Attributes()
        a.type = .directory; a.mode = 0o555; a.linkCount = 1
        a.uid = getuid(); a.gid = getgid(); a.flags = 0
        a.size = 0; a.allocSize = 0
        a.fileID = FSItem.Identifier(rawValue: item === root ? rootID : 3)!
        a.parentID = FSItem.Identifier(rawValue: item === root ? parentID : rootID)!
        a.accessTime = timespec(); a.modifyTime = timespec(); a.changeTime = timespec(); a.birthTime = timespec()
        return a
    }
    func getAttributes(_ desiredAttributes: FSItem.GetAttributesRequest, of item: FSItem,
                       replyHandler: @escaping (FSItem.Attributes?, Error?) -> Void) { replyHandler(attrs(item), nil) }
    func lookupItem(named name: FSFileName, inDirectory directory: FSItem,
                    replyHandler: @escaping (FSItem?, FSFileName?, Error?) -> Void) {
        if directory === root && name.string == "child" { replyHandler(child, name, nil) }
        else { replyHandler(nil, nil, POSIXError(.ENOENT)) }
    }
    func enumerateDirectory(_ directory: FSItem, startingAt cookie: FSDirectoryCookie,
                            verifier: FSDirectoryVerifier, attributes: FSItem.GetAttributesRequest?,
                            packer: FSDirectoryEntryPacker,
                            replyHandler: @escaping (FSDirectoryVerifier, Error?) -> Void) {
        if directory === root && cookie.rawValue == 0 {
            _ = packer.packEntry(name: FSFileName(string: "child"), itemType: .directory,
                                 itemID: FSItem.Identifier(rawValue: 3)!, nextCookie: FSDirectoryCookie(1),
                                 attributes: attributes == nil ? nil : attrs(child))
        }
        replyHandler(FSDirectoryVerifier(0), nil)
    }
    func open(_ item: FSItem, modes: FSVolume.OpenModes, cacheMode: FSVolume.DataCacheMode,
              context: FSContext, replyHandler: @escaping @Sendable (FSOpenItemResult?, Error?) -> Void) {
        replyHandler(FSOpenItemResult(grantedCoherency: cacheMode == .none || (rootNoCache && item === root) ? .noCache : .readCache), nil)
    }
    func upgrade(_ item: FSItem, cacheMode: FSVolume.DataCacheMode, context: FSContext,
                 replyHandler: @escaping @Sendable (FSUpgradeItemResult?, Error?) -> Void) {
        replyHandler(FSUpgradeItemResult(grantedCoherency: cacheMode == .none || (rootNoCache && item === root) ? .noCache : .readCache), nil)
    }
    func close(_ item: FSItem, context: FSContext, replyHandler: @escaping @Sendable () -> Void) { replyHandler() }
    func setAttributes(
        _ newAttributes: FSItem.SetAttributesRequest,
        on item: FSItem,
        replyHandler: @escaping (FSItem.Attributes?, Error?) -> Void
    ) {
        replyHandler(nil, POSIXError(.EROFS))
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

    @objc func write(
        contents: Data,
        to item: FSItem,
        at offset: off_t,
        replyHandler: @escaping (Int, (any Error)?) -> Void
    ) {
        replyHandler(0, POSIXError(.EROFS))
    }
}
