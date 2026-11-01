import AppKit
import SwiftUI

struct ContentView: View {
    private var cacheRefreshDescription: String {
        #if FUSEDOCK_FSKIT27
        if #available(macOS 27.0, *) {
            return "Remote content changes use the system cache refresh API."
        }
        #endif
        return "To refresh files changed remotely, unmount and mount the account again."
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 18) {
            HStack(spacing: 14) {
                Image(systemName: "externaldrive.connected.to.line.below")
                    .font(.system(size: 38))
                    .foregroundStyle(.tint)
                VStack(alignment: .leading, spacing: 3) {
                    Text("FuseDock").font(.largeTitle.bold())
                    Text("Native user-space file systems with FSKit")
                        .foregroundStyle(.secondary)
                }
            }

            Divider()

            Text("Enable the file system extension")
                .font(.headline)
            Text("System Settings → General → Login Items & Extensions → By Category → File System Extensions → FuseDock")
                .fixedSize(horizontal: false, vertical: true)
            Button("Open File System Extensions") {
                let url = URL(string: "x-apple.systempreferences:com.apple.ExtensionsPreferences?extensionPointIdentifier=com.apple.fskit.fsmodule")!
                NSWorkspace.shared.open(url)
            }

            Text("Mount an account")
                .font(.headline)
            Text("bin/fusedock broker start --account pku --config ~/.config/fusedock/pku.broker.credentials")
                .font(.system(.body, design: .monospaced))
                .textSelection(.enabled)
            Text("mount -t fusedock ~/.config/fusedock/pku.broker.credentials ~/FuseDockPKU")
                .font(.system(.body, design: .monospaced))
                .textSelection(.enabled)
            Text("The broker holds the Keychain-backed account and exposes a one-use, read-only capability. The password and refresh token never enter the extension.")
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)

            Text(cacheRefreshDescription)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)

            Text("Read-only · FSKit")
                .font(.caption)
                .foregroundStyle(.tertiary)
        }
        .padding(28)
        .frame(width: 620)
    }
}
