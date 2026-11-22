# Gomar application dependency notices

Gomar uses the [repository BSD 3-Clause license](../LICENSE). These notices
cover the application module and the Vue runtime. The [synthetic chat preview
notices](../THIRD_PARTY_NOTICES.md) apply to the separate root module.

| Dependency | Version | Notice |
| --- | --- | --- |
| github.com/jackc/pgx/v5 | v5.13.0 | [LICENSE](third_party/pgx/LICENSE) |
| github.com/jackc/pgpassfile | v1.0.0 | [LICENSE](third_party/pgpassfile/LICENSE) |
| github.com/jackc/pgservicefile | v0.0.0-20240606120523-5a60cdf6a761 | [LICENSE](third_party/pgservicefile/LICENSE) |
| github.com/jackc/puddle/v2 | v2.2.3 | [LICENSE](third_party/puddle/LICENSE) |
| golang.org/x/sync | v0.22.0 | [LICENSE](third_party/x-sync/LICENSE), [PATENTS](third_party/x-sync/PATENTS) |
| golang.org/x/text | v0.41.0 | [LICENSE](third_party/x-text/LICENSE), [PATENTS](third_party/x-text/PATENTS) |
| github.com/nyaruka/phonenumbers/v2 | v2.0.16 | [LICENSE](third_party/phonenumbers/LICENSE) |
| Google libphonenumber numbering-plan metadata | v9.0.42 | [LICENSE](third_party/libphonenumber/LICENSE) |
| google.golang.org/protobuf | v1.36.11 | [LICENSE](third_party/protobuf/LICENSE), [PATENTS](third_party/protobuf/PATENTS) |
| vue | 3.5.43 | [LICENSE](third_party/vue/LICENSE) |
| @vue/runtime-core | 3.5.43 | [LICENSE](third_party/runtime-core/LICENSE) |
| @vue/runtime-dom | 3.5.43 | [LICENSE](third_party/runtime-dom/LICENSE) |
| @vue/reactivity | 3.5.43 | [LICENSE](third_party/reactivity/LICENSE) |
| @vue/shared | 3.5.43 | [LICENSE](third_party/shared/LICENSE) |

The distribution also includes Django's [common-password validation
list](admin-password-data/README.md) with its [BSD 3-Clause
notice](admin-password-data/DJANGO-LICENSE). It contains public validation
data only, not account records. The Go standard library notice and patent
grant should accompany binary distributions built with that toolchain.

See `go.mod`, `go.sum` and `../frontend/package-lock.json` for exact build
dependencies. The repository does not redistribute `node_modules`.
