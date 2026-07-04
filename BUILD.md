# Build & run

```bash
go build -o determined ./cmd/determined
./determined                       # droid in a directory containing PLAN.md / STEPS.md
./determined --tool pi             # use the pi CLI instead
./determined --tool claude         # use the claude CLI instead
./determined --model claude-opus-4-7   # use a specific droid model
./determined --tool claude --model opus # use a specific claude model alias
./determined -t 2h                 # raise the time budget
./determined --max-duration 0      # no time budget (stall/failure caps still apply)
./determined --version             # print the semantic version and exit
./determined update                # update this binary from the latest GitHub Release
./determined --plan "build a todo CLI"   # interview, then write PLAN.md / STEPS.md
```

## Versioned release build

`make build` compiles the binary inside `Dockerfile.build` and stamps it with a
semantic version, dropping the result at `bin/determined`:

```bash
make build                 # uses the seed in ./VERSION (1.0.0)
make build VERSION=1.2.3    # override the version
make build TARGETOS=darwin TARGETARCH=arm64
```

The semver seed lives in the `VERSION` file (major.minor). On every push to the
default branch, the `build` GitHub Actions workflow stamps Linux ARM64, Linux
AMD64, and macOS ARM64 binaries with `MAJOR.MINOR.<run-number>`, uploads them as
a workflow artifact, and publishes them as tagged GitHub Release assets.

`determined update` uses those GitHub Release assets. It supports the platforms
published by the workflow: Linux AMD64, Linux ARM64, and macOS ARM64.
