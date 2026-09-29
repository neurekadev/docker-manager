# Tests

Binding conventions (split out of CLAUDE.md). Read this file when your change touches this area.

- The standard suite is format/lint plus isolated unit tests (owner decision,
  2026-09-25). Unit tests live next to the code, are Docker-free and
  deterministic: `go test ./...`
  runs all of them. They may use in-process fakes (`enginefake`,
  `restictest`, `regclient/regtest`, `streammux/muxtest`, `migrationtest`,
  `containerio/ciotest`), `httptest` servers, temporary directories and
  SQLite files under `t.TempDir()`; they never start containers or a Docker
  Engine, browsers, real registries, restic or other external programs
  (the restic runner's test re-executes the test binary as a fake), and
  never leave the loopback interface.
- Never add build-tagged (`integration`, `e2e`, ...), fuzz, race,
  benchmark, performance, smoke, end-to-end or other extended tests or their
  infrastructure without an explicit request; such a check stays outside
  CI.
- Time: production code takes a `clock.Clock` (`internal/clock`); tests use
  `testutil.FakeClock()` / `clock.NewFake`, `BlockUntilWaiters` + `Advance`.
  **No sleeps in assertions**, no `time.Now()` in logic tests depend on.
- Helpers: `testutil.Logger(t)`, `testutil.CaptureLogger()`,
  `testutil.Context(t)`, `migrationtest.WithFailing(...)`.
- Job steps that need what a completed step recorded read it from
  `sc.Output()`; a resumed attempt gets it back (`Job.ResumeOutput`).
  Crash recovery is covered by the job engine's unit tests (journal
  replay, `Recover`), not by killing processes.
