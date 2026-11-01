// The FSKit resource lifecycle follows Apple's PassthroughFS sample:
// https://developer.apple.com/documentation/fskit/building-a-passthrough-file-system
// See macos/FSKit/APPLE-SAMPLE-LICENSE.txt.

import Foundation
import FSKit
import OSLog

extension Logger {
    static let fusedock = Logger(subsystem: "dev.fusedock.fskit", category: "filesystem")
}

@objc
final class FuseDockFileSystem: FSUnaryFileSystem & FSUnaryFileSystemOperations {
    private var resource: FSPathURLResource?

    func probeResource(
        resource: FSResource,
        replyHandler: @escaping (FSProbeResult?, (any Error)?) -> Void
    ) {
        guard let pathResource = resource as? FSPathURLResource else {
            replyHandler(nil, POSIXError(.ENODEV))
            return
        }
        let name = pathResource.url.deletingPathExtension().lastPathComponent
        let identifier = FSContainerIdentifier(uuid: UUID())
        replyHandler(.usable(name: "FuseDock: \(name)", containerID: identifier), nil)
    }

    func loadResource(
        resource: FSResource,
        options: FSTaskOptions,
        replyHandler: @escaping (FSVolume?, (any Error)?) -> Void
    ) {
        guard let pathResource = resource as? FSPathURLResource else {
            replyHandler(nil, POSIXError(.EINVAL))
            return
        }
        if options.taskOptions.contains(where: { $0.contains("-f") }) {
            replyHandler(nil, POSIXError(.ENOTSUP))
            return
        }
        guard pathResource.url.startAccessingSecurityScopedResource() else {
            replyHandler(nil, POSIXError(.EACCES))
            return
        }

        let configPath = pathResource.url.path
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            do {
                let volume: FuseDockFSVolume
                #if FUSEDOCK_FSKIT27
                if #available(macOS 27.0, *) {
                    volume = try FuseDockCachedFSVolume(configPath: configPath)
                } else {
                    volume = try FuseDockCompatibilityFSVolume(configPath: configPath)
                }
                #else
                volume = try FuseDockCompatibilityFSVolume(configPath: configPath)
                #endif
                self?.resource = pathResource
                self?.containerStatus = .ready
                replyHandler(volume, nil)
            } catch {
                pathResource.url.stopAccessingSecurityScopedResource()
                Logger.fusedock.error("loadResource failed: \(error.localizedDescription, privacy: .public)")
                replyHandler(nil, error)
            }
        }
    }

    func unloadResource(
        resource: FSResource,
        options: FSTaskOptions,
        replyHandler: @escaping ((any Error)?) -> Void
    ) {
        guard let pathResource = resource as? FSPathURLResource else {
            replyHandler(POSIXError(.EINVAL))
            return
        }
        guard let loaded = self.resource, loaded.url == pathResource.url else {
            replyHandler(POSIXError(.EINVAL))
            return
        }
        loaded.url.stopAccessingSecurityScopedResource()
        self.resource = nil
        replyHandler(nil)
    }
}
