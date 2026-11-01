# Agent inspection

`kasuri-agent-inspect` reads a checkout's local agent invocation history for
debugging. It never constructs a service, configures a provider or writes local
metadata. Build it with `make build-agents` (`make build-agents-linux`
cross-builds it for Linux amd64 without CGO); `make build` includes it.

Inspection accepts current version 2 journals and readable version 1 history.
It never triggers migration or resumes an invocation. The first admitted
journal mutation handles migration through the store; see
[journal migration and recovery](../CONFIG.md#local-metadata-and-manual-cleanup).

```sh
build/kasuri-agent-inspect --root /absolute/checkout --limit 100
build/kasuri-agent-inspect --root /absolute/checkout --run INVOCATION_ID
build/kasuri-agent-inspect --root /absolute/checkout --run INVOCATION_ID --private
build/kasuri-agent-inspect --root /absolute/checkout --artifact ARTIFACT_ID --private
```

Default JSON contains recorded invocation ancestry, schema/state, node events,
usage and artifact metadata. It does not infer an unrecorded dependency graph or
executed check. `--run` filters runs and their events; `--artifact` filters the
artifact section. Other sections remain metadata only. IDs are taken from the
metadata output. The two selections cannot be combined.

Private content requires `--private` with one exact ID. A selected run exposes
its private session, including shared app/user state returned by the journal
owner. A selected artifact exposes that immutable version as `data_base64`.
Treat these outputs as source-bearing material. Unknown IDs and corrupt journals
fail rather than returning empty history; no repair, resume or migration occurs.

The inspector never constructs a service, configures a provider or writes local
metadata. An empty checkout returns empty history without creating `.kasuri`.
It reports `truncated` when the per-section limit (1–500) omits records, alongside
available run/artifact counts. Output over 8 MiB fails before writing JSON; narrow
the selection. History retains only the journal's existing bounded event window.
Concurrent writers may advance the journal between section reads. Exit status is
0 for success/help, 2 for command-line usage errors, and 1 for inspection/output
failures.
