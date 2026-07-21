# Build & run

```bash
go build -o determined ./cmd/determined
./determined -exec                 # droid in a directory containing PLAN.md / STEPS.md
./determined -exec --tool pi       # use the pi CLI instead
./determined -exec --tool claude   # use the claude CLI instead
./determined -exec --model claude-opus-4-7   # use a specific droid model
./determined -exec --tool claude --model opus # use a specific claude model alias
./determined -exec -t 2h           # raise the time budget
./determined -exec --max-duration 0 # no time budget (stall/failure caps still apply)
./determined --version             # print the semantic version and exit
./determined update                # update this binary from the latest GitHub Release
./determined --plan "build a todo CLI"   # interview, then write PLAN.md / STEPS.md
./determined --plan "build a todo CLI" -exec # plan, then execute in one run
./determined --plan "try a todo UI" -prototype # fast experiment, minimal detail
./determined --review-plan              # interview, critique, and revise an existing plan
./determined                       # no operation flag: default to -exec
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
