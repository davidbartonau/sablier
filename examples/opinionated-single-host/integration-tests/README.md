# VRAM eviction integration tests

Scripts that drive Sablier's VRAM-aware eviction policy end-to-end against
bare `nginx:alpine` containers (separate from the full nginx + Sablier
stack in the parent directory).

For prerequisites, run instructions, scenario coverage, and the test
report itself, see **`VRAM_TEST_REPORT.html`** in the repo root.

Quick start, from the repo root:

```
go build -buildvcs=false -o ./bin/sablier ./cmd/sablier
./examples/opinionated-single-host/integration-tests/run.sh
```
