package protocol

import "time"

// Container logs and exec (#8): inputs and outputs of the container.logs
// and container.exec.* requests and of the container.logs and
// container.exec streams. The manager authorizes (container.logs.read,
// container.exec, #17); the agent runs only named operations on the
// Engine through the Moby adapter (no host shell, no API passthrough).

// Log bounds.
const (
	// DefaultLogTail is the tail of a log request without one.
	DefaultLogTail = 500
	// MaxLogTail bounds the lines of one container.logs answer.
	MaxLogTail = 5000
	// MaxLogBytes bounds the log bytes of one container.logs answer (base64
	// in the frame); older lines are dropped first.
	MaxLogBytes = 600 << 10
	// MaxLogLine: longer lines are split into parts of this size (Partial
	// on all but the last).
	MaxLogLine = 16 << 10
)

// Log stream names.
const (
	LogStdout = "stdout"
	LogStderr = "stderr"
)

// ContainerLogsInput selects a container's log output.
type ContainerLogsInput struct {
	// ContainerID is the container's ID or name.
	ContainerID string `json:"containerId"`
	// Tail is the number of lines from the end (0: DefaultLogTail for the
	// request, 200 for a stream; at most MaxLogTail).
	Tail int `json:"tail,omitempty"`
	// Since and Until bound the lines by timestamp (inclusive); Since is
	// also the resume cursor of a stream.
	Since time.Time `json:"since,omitzero"`
	Until time.Time `json:"until,omitzero"`
	// Stdout and Stderr select the output streams (neither: both).
	Stdout bool `json:"stdout,omitempty"`
	Stderr bool `json:"stderr,omitempty"`
}

// LogLine is one line (or part of a long line) of container output. It
// is also the NDJSON record of the container.logs stream.
type LogLine struct {
	At      time.Time `json:"at"`
	Stream  string    `json:"stream"`
	Data    []byte    `json:"data"`
	Partial bool      `json:"partial,omitempty"`
}

// ContainerLogsOutput is the answer of container.logs.
type ContainerLogsOutput struct {
	Lines []LogLine `json:"lines"`
	// Truncated: older lines were left out to stay within MaxLogBytes.
	Truncated bool `json:"truncated,omitempty"`
}

// ExecCreateInput creates an exec instance in a running container.
type ExecCreateInput struct {
	ContainerID string `json:"containerId"`
	// Cmd is the argv to run inside the container (never a host shell).
	Cmd        []string `json:"cmd"`
	Tty        bool     `json:"tty"`
	Cols       uint     `json:"cols,omitempty"`
	Rows       uint     `json:"rows,omitempty"`
	WorkingDir string   `json:"workingDir,omitempty"`
	User       string   `json:"user,omitempty"`
}

// ExecCreateOutput names the created exec instance.
type ExecCreateOutput struct {
	ExecID string `json:"execId"`
}

// ExecResizeInput resizes an exec's terminal.
type ExecResizeInput struct {
	ExecID string `json:"execId"`
	Cols   uint   `json:"cols"`
	Rows   uint   `json:"rows"`
}

// ExecDeleteInput ends an exec session: its stdin is closed. Engine exec
// instances cannot be killed; a process ignoring end of input runs on.
type ExecDeleteInput struct {
	ExecID string `json:"execId"`
}

// ExecStreamInput attaches the container.exec stream to a created exec:
// stream_data with channel stdin flows to the process, stdout/stderr
// (everything is stdout with a TTY) back; the agent's final stream_close
// carries the exit code.
type ExecStreamInput struct {
	ExecID string `json:"execId"`
}

// Exec bounds.
const (
	// MaxExecArgs and MaxExecArgBytes bound an exec command.
	MaxExecArgs     = 256
	MaxExecArgBytes = 64 << 10
)
