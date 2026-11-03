# Kogin release fixture

This intentionally small project exercises source indexing, Go replace/create
targets (`Run`, `Worker`, and an absent new declaration), ignored secrets, and
successful Go tests. It also contains parser, vet, test, and AI-suggestion
fixture inputs for the verified-finding and provenance checks.

Files ending in `.fixture` are not compiled by the project itself. The release
checklist copies each relevant case into an isolated temporary Go project before
running a parser, `go vet`, or `go test` scan. This keeps the normal fixture
usable for the complete replace/create → Apply → Undo flow while still making
the expected failure cases reproducible.
The `.env`, certificate-shaped file, and configuration file contain placeholders
only; they exist to prove the context policy excludes them. Tests copy this
fixture to a temporary directory before importing it. Git-specific acceptance
checks initialize one copied variant as a repository and leave another as a
non-Git project.
