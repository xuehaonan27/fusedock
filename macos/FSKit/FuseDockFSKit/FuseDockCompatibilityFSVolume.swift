import Foundation
import FSKit

// macOS 26 keeps each file's observed version for the FSItem lifetime. The
// kernel owns that lifetime; unmount/remount is the explicit refresh boundary.
final class FuseDockCompatibilityFSVolume: FuseDockFSVolume, FSVolume.Operations, FSVolume.ReadWriteOperations, FSVolume.OpenCloseOperations {
    func openItem(
        _ item: FSItem,
        modes: FSVolume.OpenModes,
        replyHandler: @escaping ((any Error)?) -> Void
    ) {
        guard !modes.contains(.write) else { replyHandler(POSIXError(.EROFS)); return }
        guard let fsItem = item as? FuseDockFSItem else { replyHandler(POSIXError(.EINVAL)); return }
        if fsItem.object.isDirectory { replyHandler(nil); return }
        DispatchQueue.global(qos: .userInitiated).async {
            do {
                try self.openRead(fsItem)
                replyHandler(nil)
            } catch {
                replyHandler(Self.posixError(for: error))
            }
        }
    }

    func closeItem(
        _ item: FSItem,
        modes: FSVolume.OpenModes,
        replyHandler: @escaping ((any Error)?) -> Void
    ) {
        // These are retained modes, not the modes being closed. An active
        // reader or memory mapping still needs its provider handle.
        if modes.isEmpty, let fsItem = item as? FuseDockFSItem { closeRead(fsItem) }
        replyHandler(nil)
    }
}
