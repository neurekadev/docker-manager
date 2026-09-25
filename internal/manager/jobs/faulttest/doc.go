// Package faulttest holds the job engine's crash tests (#26, #29). They
// only build with -tags faultinject:
//
//	go test -tags faultinject ./internal/manager/jobs/faulttest/
//
// The test binary re-executes itself as a manager process (real job engine
// on a SQLite file) and an agent process (real agent runner with a journal
// in a state directory, simulated step sequences mirroring deploy,
// backup-with-container-shutdown and prune, plus the manager-local
// retention and manager backup kinds). The parent relays protocol
// frames between them over stdio like the session transport would. A clean
// traced run enumerates every fault point the flow reaches; then, for each
// point, the manager or the agent is killed there (os.Exit) and restarted,
// and the test asserts: the job ends in an explicit terminal state (with
// recovery guidance unless it succeeded), no locks remain, no
// non-idempotent step ran twice, and stopped containers were started again.
//
// TestKillRealExecutorsAtEveryStage (real_test.go) does the same with the
// features' real executors (stack.deploy, backup.run with container
// shutdown, prune.run) over a file-backed in-memory Engine and restic, and
// checks the world each run left behind.
package faulttest
