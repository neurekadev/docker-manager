// Package verification checks the release verification map (#12, #29)
// without Docker: docs/testing/verification-matrix.md names real tests,
// its statuses are consistent with what can run where, every build-tagged
// test is selected by a job of the extended workflow, and the user guide
// and the security review cover their checklists.
package verification
