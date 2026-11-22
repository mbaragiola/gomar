# Third-party notices

Gomar's BSD-3-Clause license covers its own code, not the dependencies below.
These files preserve the upstream texts from the pinned module versions
used by the Codex preview.

| Module | Version | Texts |
| --- | --- | --- |
| [Model Context Protocol Go SDK](https://github.com/modelcontextprotocol/go-sdk/tree/v1.8.0) | v1.8.0 | [LICENSE](licenses/mcp-go-sdk/LICENSE) |
| [Google JSON Schema Go](https://github.com/google/jsonschema-go/tree/v0.4.3) | v0.4.3 | [LICENSE](licenses/jsonschema-go/LICENSE) |
| [Segment encoding](https://github.com/segmentio/encoding/tree/v0.5.4) | v0.5.4 | [LICENSE](licenses/segmentio-encoding/LICENSE) |
| [Segment assembly routines](https://github.com/segmentio/asm/tree/v1.1.3) | v1.1.3 | [LICENSE](licenses/segmentio-asm/LICENSE) |
| [URI Templates](https://github.com/yosida95/uritemplate/tree/v3.0.2) | v3.0.2 | [LICENSE](licenses/uritemplate/LICENSE) |
| [Go OAuth2](https://go.googlesource.com/oauth2/+/refs/tags/v0.35.0) | v0.35.0 | [LICENSE](licenses/golang-x-oauth2/LICENSE) |
| [Go sync](https://go.googlesource.com/sync/+/refs/tags/v0.20.0) | v0.20.0 | [LICENSE](licenses/golang-x-sync/LICENSE), [PATENTS](licenses/golang-x-sync/PATENTS) |
| [Go sys](https://go.googlesource.com/sys/+/refs/tags/v0.41.0) | v0.41.0 | [LICENSE](licenses/golang-x-sys/LICENSE), [PATENTS](licenses/golang-x-sys/PATENTS) |
| [Go time](https://go.googlesource.com/time/+/refs/tags/v0.15.0) | v0.15.0 | [LICENSE](licenses/golang-x-time/LICENSE), [PATENTS](licenses/golang-x-time/PATENTS) |

The SDK license describes its transition from MIT to Apache-2.0. Older
contributions without relicensing consent retain their MIT terms. The
same upstream file describes the terms for documentation. The release
does not have a separate NOTICE file.

Before publishing binaries, check the actual linked dependencies for each
target, include the notices that apply, and include the LICENSE and PATENTS
from the exact Go toolchain whose standard library was linked. If vendoring
source, also retain any notices that apply to the vendored files. The module
lock alone does not say what was compiled into a binary.
