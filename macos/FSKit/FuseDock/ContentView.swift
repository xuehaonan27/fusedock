import AppKit
import SwiftUI

struct ContentView: View {
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
            Text("System Settings → General → Login Items & Extensions → File System Extensions → FuseDock")
                .fixedSize(horizontal: false, vertical: true)
            Button("Open Login Items & Extensions") {
                let url = URL(string: "x-apple.systempreferences:com.apple.LoginItems-Settings.extension")!
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

            Text("Read-only preview · no kernel extension · no macFUSE")
                .font(.caption)
                .foregroundStyle(.tertiary)
        }
        .padding(28)
        .frame(width: 620)
    }
}
