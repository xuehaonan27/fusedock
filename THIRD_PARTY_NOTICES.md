# Third-party notices

## unofficialpku/disk

The PKU IAAA and AnyShare protocol implementation in `internal/providers/pku` is derived from the `unofficialpku/disk` project (formerly reachable through `thezzisu/pkudisk-fuse`):

- Source: https://github.com/unofficialpku/disk
- License: Apache License 2.0
- Copyright: the respective contributors

Upstream NOTICE:

```text
PKUDisk
Copyright 2026 thezzisu

Licensed under the Apache License, Version 2.0. See LICENSE.
Third-party components retain their own copyrights and licenses. Binary
release archives include their license and notice files in licenses/.

This is an independent client for Peking University NetDisk (AnyShare); it is not an official
Peking University or AnyShare product.
```

The upstream implementation was consulted for the login flow, AnyShare namespace endpoints, error behavior, and signed range-read protocol. FuseDock keeps those details inside its `pku` provider and exposes a provider-neutral contract to the rest of the application.

No account credentials, private keys, cookies, or signed storage URLs from the upstream project are included here.
