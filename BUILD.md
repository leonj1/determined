# Build & run

```bash
go build -o determined ./cmd/determined
./determined                       # droid in a directory containing PLAN.md / STEPS.md
./determined --tool pi             # use the pi CLI instead
./determined --tool claude         # use the claude CLI instead
./determined --max-duration 2h     # raise the time budget
./determined --max-duration 0      # no time budget (stall/failure caps still apply)
./determined --version             # print the semantic version and exit
./determined --plan "build a todo CLI"   # interview, then write PLAN.md / STEPS.md
```

## Versioned release build

`make build` compiles the binary inside `Dockerfile.build` and stamps it with a
semantic version, dropping the result at `bin/determined`:

```bash
make build                 # uses the seed in ./VERSION (1.0.0)
make build VERSION=1.2.3    # override the version
```

The semver seed lives in the `VERSION` file (major.minor). On every push to the
default branch, the `build` GitHub Actions workflow stamps the binary with
`MAJOR.MINOR.<run-number>`, uploads it as a workflow artifact, and publishes a
tagged GitHub Release.
