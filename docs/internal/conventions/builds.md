# Image builds (#33)

Binding conventions (split out of CLAUDE.md). Read this file when your change touches this area.

Guide: `docs/internal/architecture/builds.md`. Git credentials
(`internal/manager/gitcreds`) mirror registry connections; builds
(`internal/manager/builds`) enqueue `image.build` jobs whose input
(`jobspec.ImageBuildInput`) names credentials by ID only; the build record
ID is the job ID. Git refs are resolved in process (`internal/gitremote`,
never a git CLI) and BuildKit builds the exact commit. Steps that can stop
safely mid-way on cancellation return `jobexec.ErrStepCancelled`.
Job target `build_definition` covers the images of a definition run.
Compose build sections (`stack.build`, the deploy's `build_images` step)
build through the Compose adapter's BuildKit path (`compose.BuildOptions`
`BuildEvents`/`Built`), never the SDK's build path; build executors
stream, scrub, cancel and time out with `internal/agent/buildrun`
(`NewProgress`, `Run`, `ScrubError`).
