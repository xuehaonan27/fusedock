// Controlled content changes through FSKit only. The external state file selects
// a revision and cache action; every returned FSItem retains its own revision.
import Foundation
import FSKit
import OSLog

@available(macOS 27.0, *)
final class ReadProbeItem: FSItem {
    let revision: Int
    let nested: Bool
    var size: Int { (8 << 20) + (revision == 1 ? 17 : 32) }
    var byte: UInt8 { UInt8(64 + revision) }
    init(revision: Int, nested: Bool) {
        self.revision = revision
        self.nested = nested
        super.init()
    }
}

@available(macOS 27.0, *)
final class ReadCacheProbe: RootCacheProbe, FSVolume.ReadWriteOperations {
    let controlURL: URL
    private let stateQueue = DispatchQueue(label: "read-cache-probe.state")
    private var revision = 1
    private var files: [String: ReadProbeItem] = [:]

    override init(configPath: String) throws {
        controlURL = URL(fileURLWithPath: configPath).appendingPathComponent("state")
        try super.init(configPath: configPath)
        for revision in 1...3 {
            for nested in [false, true] {
                files["\(nested)-\(revision)"] = ReadProbeItem(revision: revision, nested: nested)
            }
        }
    }

    private func currentFile(nested: Bool) -> ReadProbeItem {
        stateQueue.sync { files["\(nested)-\(revision)"]! }
    }

    private func trace(_ event: String, item: FSItem, detail: String = "") {
        let file = item as? ReadProbeItem
        let parent = file.map { $0.nested ? "child" : "root" } ?? (item === root ? "root" : "child")
        logger.notice("probe=\(self.probeID, privacy: .public) event=\(event, privacy: .public) parent=\(parent, privacy: .public) revision=\(file?.revision ?? 0) \(detail, privacy: .public)")
    }

    override func probe() {
        guard let command = try? String(contentsOf: controlURL, encoding: .utf8) else { return }
        let fields = command.split(whereSeparator: { $0.isWhitespace })
        guard fields.count == 2, let next = Int(fields[0]), (1...3).contains(next) else { return }
        let action = String(fields[1])
        let old = stateQueue.sync { () -> [ReadProbeItem] in
            if revision == next { return [] }
            let old = [files["false-\(revision)"]!, files["true-\(revision)"]!]
            revision = next
            return old
        }
        if old.isEmpty { return }
        let targets: [FSItem]
        switch action {
        case "directory": targets = [root, child]
        case "file", "revoke": targets = old
        default: targets = []
        }
        for item in targets {
            let error = setCacheState(for: item, cacheMode: .readWithCache, coherencyType: .readCache,
                                      action: action == "revoke" ? .revoke : .invalidate) as NSError?
            trace("invalidate", item: item, detail: "action=\(action) code=\(error?.code ?? 0)")
        }
        logger.notice("probe=\(self.probeID, privacy: .public) event=published revision=\(next) action=\(action, privacy: .public)")
    }

    override func attrs(_ item: FSItem) -> FSItem.Attributes {
        guard let file = item as? ReadProbeItem else { return super.attrs(item) }
        let a = super.attrs(item)
        a.type = .file; a.mode = 0o444
        a.size = UInt64(file.size); a.allocSize = a.size
        a.fileID = FSItem.Identifier(rawValue: file.nested ? 5 : 4)!
        a.parentID = FSItem.Identifier(rawValue: file.nested ? 3 : rootID)!
        trace("attributes", item: item)
        return a
    }

    override func lookupItem(named name: FSFileName, inDirectory directory: FSItem,
                             replyHandler: @escaping (FSItem?, FSFileName?, Error?) -> Void) {
        if name.string == "file.txt" || name.string == "fresh.txt" {
            let file = currentFile(nested: directory === child)
            trace("lookup", item: file, detail: "name=\(name.string ?? "")")
            replyHandler(file, name, nil)
        } else {
            super.lookupItem(named: name, inDirectory: directory, replyHandler: replyHandler)
        }
    }

    override func enumerateDirectory(_ directory: FSItem, startingAt cookie: FSDirectoryCookie,
                                     verifier: FSDirectoryVerifier, attributes: FSItem.GetAttributesRequest?,
                                     packer: FSDirectoryEntryPacker,
                                     replyHandler: @escaping (FSDirectoryVerifier, Error?) -> Void) {
        let file = currentFile(nested: directory === child)
        let entries: [(String, FSItem)] = directory === root ? [("child", child), ("file.txt", file)] : [("file.txt", file)]
        for (index, entry) in entries.enumerated() where index >= Int(cookie.rawValue) {
            let a = attrs(entry.1)
            if !packer.packEntry(name: FSFileName(string: entry.0), itemType: a.type, itemID: a.fileID,
                                 nextCookie: FSDirectoryCookie(UInt64(index + 1)),
                                 attributes: attributes == nil ? nil : a) { break }
        }
        replyHandler(FSDirectoryVerifier(0), nil)
    }

    override func open(_ item: FSItem, modes: FSVolume.OpenModes, cacheMode: FSVolume.DataCacheMode,
                       context: FSContext, replyHandler: @escaping @Sendable (FSOpenItemResult?, Error?) -> Void) {
        trace("open", item: item)
        super.open(item, modes: modes, cacheMode: cacheMode, context: context, replyHandler: replyHandler)
    }

    override func close(_ item: FSItem, context: FSContext, replyHandler: @escaping @Sendable () -> Void) {
        trace("close", item: item)
        replyHandler()
    }

    override func reclaimItem(_ item: FSItem, replyHandler: @escaping (Error?) -> Void) {
        trace("reclaim", item: item)
        replyHandler(nil)
    }

    func read(from item: FSItem, at offset: off_t, length: Int, into buffer: FSMutableFileDataBuffer,
              replyHandler: @escaping (Int, Error?) -> Void) {
        guard let file = item as? ReadProbeItem else { replyHandler(0, POSIXError(.EISDIR)); return }
        let count = min(length, max(0, file.size - Int(offset)))
        if count > 0 {
            buffer.withUnsafeMutableBytes { bytes in
                _ = bytes.baseAddress!.initializeMemory(as: UInt8.self, repeating: file.byte, count: count)
            }
        }
        trace("read", item: item, detail: "offset=\(offset) count=\(count)")
        replyHandler(count, nil)
    }
}
