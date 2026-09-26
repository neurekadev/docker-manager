package restic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Runner executes the restic binary. It is the only process execution in
// Docker Manager's backup code (see the package documentation for the rules).
type Runner struct {
	// Binary is the restic executable (default DefaultBinary).
	Binary string
	// CacheDir is restic's cache (RESTIC_CACHE_DIR); empty disables it.
	CacheDir string
	// TempDir holds temporary files (TMPDIR, and the password files on
	// systems without /proc/self/fd); empty uses os.TempDir().
	TempDir string
	// Logger receives one debug record per call (operation, exit code,
	// duration; never arguments with values, environment or output).
	Logger *slog.Logger
	// KillGrace is how long a cancelled restic may clean up after SIGINT
	// before it is killed (default 30 s).
	KillGrace time.Duration
	// RetryLock waits this long for a repository lock (default 2 min).
	RetryLock time.Duration
}

// Output bounds.
const (
	maxCollected  = 64 << 20 // snapshots/key lists
	maxLine       = 4 << 20
	maxStderrTail = 8 << 10
	maxErrors     = 20
)

// Open implements Opener.
func (r *Runner) Open(loc Location, password string) Repo {
	return &repo{r: r, loc: loc, password: password}
}

type repo struct {
	r        *Runner
	loc      Location
	password string
}

// call is one restic invocation.
type call struct {
	op   string
	args []string
	// stdin is the child's standard input (nil: none).
	stdin io.Reader
	dir   string
	// newPassword is delivered like the password (key add).
	newPassword string
	// lines receives each stdout line (JSON message streams); otherwise
	// stdout is collected (bounded) or copied to raw.
	lines func(line []byte)
	raw   io.Writer
	// ok lists additional exit codes that are not failures (backup: 3).
	ok []int
}

type result struct {
	stdout   []byte
	exitCode int
}

func (p *repo) secrets() []string {
	return append([]string{p.password}, p.loc.Secrets()...)
}

func (p *repo) run(ctx context.Context, c call) (result, error) {
	r := p.r
	bin := r.Binary
	if bin == "" {
		bin = DefaultBinary
	}
	secrets := p.secrets()
	if c.newPassword != "" {
		secrets = append(secrets, c.newPassword)
	}
	tmp := r.TempDir
	if tmp == "" {
		tmp = os.TempDir()
	}
	// restic stages pack files in TMPDIR; a fresh data volume has no tmp
	// directory yet (and on Linux the secrets never touch it either).
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return result{}, &Error{Op: c.op, Code: CodeFailed, Message: "prepare the temporary directory: " + scrub(err.Error(), secrets)}
	}
	files, err := newSecretFiles(tmp)
	if err != nil {
		return result{}, &Error{Op: c.op, Code: CodeFailed, Message: "prepare secret delivery: " + scrub(err.Error(), secrets)}
	}
	defer files.cleanup()
	pwPath, err := files.add(p.password)
	if err != nil {
		return result{}, &Error{Op: c.op, Code: CodeFailed, Message: "deliver the repository password: " + scrub(err.Error(), secrets)}
	}
	args := p.globalArgs()
	args = append(args, c.args...)
	if c.newPassword != "" {
		np, err := files.add(c.newPassword)
		if err != nil {
			return result{}, &Error{Op: c.op, Code: CodeFailed, Message: "deliver the new password: " + scrub(err.Error(), secrets)}
		}
		args = append(args, "--new-password-file", np)
	}
	// runCtx also ends when restic reports a retry of a permanent backend
	// failure (see retryWatch).
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	cmd := exec.CommandContext(runCtx, bin, args...) //nolint:gosec // fixed binary; arguments carry no secrets and no shell
	cmd.Env = p.env(pwPath, tmp)
	cmd.Dir = c.dir
	cmd.Stdin = c.stdin
	cmd.ExtraFiles = files.extra
	cmd.Cancel = func() error {
		if runtime.GOOS == "windows" {
			return cmd.Process.Kill()
		}
		return cmd.Process.Signal(os.Interrupt)
	}
	grace := r.KillGrace
	if grace <= 0 {
		grace = 30 * time.Second
	}
	cmd.WaitDelay = grace
	stderr := &tailBuffer{max: maxStderrTail}
	watch := &retryWatch{tail: stderr, abort: stop}
	cmd.Stderr = watch
	var collected bytes.Buffer
	var stdoutErr error
	var stdout io.ReadCloser
	switch {
	case c.raw != nil:
		cmd.Stdout = c.raw
	default:
		if stdout, err = cmd.StdoutPipe(); err != nil {
			return result{}, &Error{Op: c.op, Code: CodeFailed, Message: scrub(err.Error(), secrets)}
		}
	}
	start := time.Now()
	if err := cmd.Start(); err != nil {
		code := CodeFailed
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			code = CodeUnavailable
		}
		return result{}, &Error{Op: c.op, Code: code, Message: scrub(err.Error(), secrets)}
	}
	files.started()
	if stdout != nil {
		if c.lines != nil {
			stdoutErr = readLines(stdout, c.lines)
		} else {
			_, stdoutErr = io.Copy(&collected, io.LimitReader(stdout, maxCollected+1))
			if collected.Len() > maxCollected {
				stdoutErr = fmt.Errorf("output larger than %d bytes", maxCollected)
			}
			_, _ = io.Copy(io.Discard, stdout)
		}
	}
	waitErr := cmd.Wait()
	exit := 0
	if cmd.ProcessState != nil {
		exit = cmd.ProcessState.ExitCode()
	}
	if r.Logger != nil {
		r.Logger.Debug("restic", "op", c.op, "exit_code", exit, "duration", time.Since(start).Round(time.Millisecond).String())
	}
	res := result{stdout: collected.Bytes(), exitCode: exit}
	if ctx.Err() != nil {
		return res, &Error{Op: c.op, Code: CodeCancelled, ExitCode: exit, Message: "cancelled"}
	}
	// Written by the stderr copier, which cmd.Wait has joined.
	if watch.code != "" {
		return res, &Error{Op: c.op, Code: watch.code, ExitCode: exit, Message: summarize(stderr.String(), secrets)}
	}
	if waitErr != nil && !slices.Contains(c.ok, exit) {
		return res, classify(c.op, exit, stderr.String(), secrets)
	}
	if stdoutErr != nil {
		return res, &Error{Op: c.op, Code: CodeFailed, ExitCode: exit, Message: "read output: " + scrub(stdoutErr.Error(), secrets)}
	}
	return res, nil
}

func (p *repo) globalArgs() []string {
	var args []string
	if p.loc.S3 != nil && p.loc.S3.PathStyle {
		args = append(args, "-o", "s3.bucket-lookup=path")
	}
	if p.r.CacheDir == "" {
		args = append(args, "--no-cache")
	}
	lock := p.r.RetryLock
	if lock <= 0 {
		lock = 2 * time.Minute
	}
	args = append(args, "--retry-lock", lock.String())
	return args
}

// env builds the child's environment from scratch.
func (p *repo) env(passwordFile, tmp string) []string {
	env := []string{
		"RESTIC_REPOSITORY=" + p.loc.Repository,
		"RESTIC_PASSWORD_FILE=" + passwordFile,
		// JSON status lines every 5 s (restic defaults to 60 per second).
		"RESTIC_PROGRESS_FPS=0.2",
		"TMPDIR=" + tmp,
		"HOME=" + tmp,
	}
	if p.r.CacheDir != "" {
		env = append(env, "RESTIC_CACHE_DIR="+p.r.CacheDir)
	}
	if s3 := p.loc.S3; s3 != nil {
		env = append(env, "AWS_ACCESS_KEY_ID="+s3.AccessKeyID, "AWS_SECRET_ACCESS_KEY="+s3.SecretAccessKey)
		if s3.Region != "" {
			env = append(env, "AWS_DEFAULT_REGION="+s3.Region)
		}
	}
	if runtime.GOOS == "windows" {
		// Windows processes need SYSTEMROOT to load system libraries.
		if v, ok := os.LookupEnv("SYSTEMROOT"); ok {
			env = append(env, "SYSTEMROOT="+v)
		}
		env = append(env, "TEMP="+tmp, "TMP="+tmp, "USERPROFILE="+tmp, "LOCALAPPDATA="+tmp)
	}
	return env
}

// readLines calls fn for every line of r (bounded line length).
func readLines(r io.Reader, fn func([]byte)) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var line []byte
	for {
		chunk, isPrefix, err := br.ReadLine()
		if len(chunk) > 0 {
			if len(line)+len(chunk) > maxLine {
				_, _ = io.Copy(io.Discard, br)
				return fmt.Errorf("output line longer than %d bytes", maxLine)
			}
			line = append(line, chunk...)
		}
		if err == io.EOF {
			if len(line) > 0 {
				fn(line)
			}
			return nil
		}
		if err != nil {
			return err
		}
		if !isPrefix {
			fn(line)
			line = line[:0]
		}
	}
}

// classify maps a failed exit to an *Error.
func classify(op string, exit int, stderr string, secrets []string) *Error {
	msg := summarize(stderr, secrets)
	code := CodeFailed
	switch exit {
	case 10:
		code = CodeRepositoryNotFound
	case 11:
		code = CodeLocked
	case 12:
		code = CodeKeyRejected
	case 130:
		code = CodeCancelled
	}
	if code == CodeFailed {
		code = classifyText(strings.ToLower(stderr))
	}
	return &Error{Op: op, Code: code, ExitCode: exit, Message: msg}
}

func classifyText(s string) string {
	has := func(subs ...string) bool {
		for _, sub := range subs {
			if strings.Contains(s, sub) {
				return true
			}
		}
		return false
	}
	switch {
	case has("wrong password", "no key found"):
		return CodeKeyRejected
	case has("config file already exists", "repository master key and config already initialized"):
		return CodeRepositoryExists
	case has("access denied", "accessdenied", "invalidaccesskeyid", "signaturedoesnotmatch", "403 forbidden", "status code: 403",
		// S3 error messages (minio-go prints the message, not the code).
		"signature we calculated does not match", "access key id you provided does not exist", "difference between the request time"):
		return CodeAccessDenied
	case has("repository does not exist", "unable to open config file", "is there a repository at the following location", "nosuchbucket", "bucket does not exist"):
		return CodeRepositoryNotFound
	case has("repository is already locked", "unable to create lock"):
		return CodeLocked
	case has("no matching id found", "no snapshot found", "snapshot not found", "path not found", "not found in snapshot", "cannot dump file"):
		return CodeSnapshotNotFound
	case has("no such host", "connection refused", "i/o timeout", "network is unreachable", "tls handshake", "dial tcp"):
		return CodeUnreachable
	case has("repository contains errors", "pack file cannot be listed", "error for tree", "ciphertext verification failed",
		"load(<data", "unexpected eof", "checksum mismatch", "invalid data returned", "wrong hash"):
		return CodeRepositoryDamaged
	}
	return CodeFailed
}

// summarize keeps the last lines of stderr (bounded, secrets scrubbed).
func summarize(stderr string, secrets []string) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	var keep []string
	for i := len(lines) - 1; i >= 0 && len(keep) < 3; i-- {
		l := strings.TrimSpace(lines[i])
		if strings.HasPrefix(l, "{") {
			// With --json, restic reports fatal errors as
			// {"message_type":"exit_error","code":N,"message":"..."}.
			var m struct {
				MessageType string `json:"message_type"`
				Message     string `json:"message"`
			}
			if json.Unmarshal([]byte(l), &m) != nil || m.Message == "" {
				continue
			}
			l = m.Message
		}
		if l == "" {
			continue
		}
		keep = append([]string{l}, keep...)
	}
	msg := strings.Join(keep, "; ")
	if len(msg) > 512 {
		msg = msg[:512] + "…"
	}
	return scrub(msg, secrets)
}

// scrub replaces every secret value in s.
func scrub(s string, secrets []string) string {
	for _, v := range secrets {
		if len(v) >= 4 {
			s = strings.ReplaceAll(s, v, "[redacted]")
		}
	}
	return s
}

// retryNotice is how restic reports a backend request it will retry
// ("Load(<config/0000000000>, 0, 0) returned error, retrying after
// 1.2s: <error>"). restic retries every error its backend does not
// consider permanent for up to 15 minutes and has no option to shorten
// that; the S3 backend treats only AccessDenied as permanent, so a wrong
// secret key (SignatureDoesNotMatch), an unknown key ID or a missing
// bucket would hold a job for 15 minutes.
const retryNotice = "returned error, retrying after"

// permanentRetry returns the error class of a retry notice whose error
// no retry can fix (access denied, missing bucket), or "".
func permanentRetry(line string) string {
	s := strings.ToLower(line)
	_, cause, ok := strings.Cut(s, retryNotice)
	if !ok {
		return ""
	}
	switch code := classifyText(cause); code {
	case CodeAccessDenied, CodeRepositoryNotFound:
		return code
	}
	return ""
}

// retryWatch is restic's stderr: it keeps the tail for error messages and
// ends the run (abort) at the first retry of a permanent failure, keeping
// its class in code (read after cmd.Wait).
type retryWatch struct {
	tail    *tailBuffer
	abort   func()
	partial []byte
	code    string
}

func (w *retryWatch) Write(p []byte) (int, error) {
	_, _ = w.tail.Write(p)
	if w.code != "" {
		return len(p), nil
	}
	w.partial = append(w.partial, p...)
	for {
		line, rest, ok := bytes.Cut(w.partial, []byte("\n"))
		if !ok {
			break
		}
		w.partial = rest
		if code := permanentRetry(string(line)); code != "" {
			w.code, w.partial = code, nil
			w.abort()
			break
		}
	}
	if len(w.partial) > maxStderrTail {
		w.partial = w.partial[len(w.partial)-maxStderrTail:]
	}
	return len(p), nil
}

// tailBuffer keeps the last max bytes written.
type tailBuffer struct {
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return string(t.buf) }

// --- operations ---

type message struct {
	MessageType string  `json:"message_type"`
	PercentDone float64 `json:"percent_done"`
	TotalFiles  int64   `json:"total_files"`
	FilesDone   int64   `json:"files_done"`
	TotalBytes  int64   `json:"total_bytes"`
	BytesDone   int64   `json:"bytes_done"`
	// summary (backup)
	SnapshotID          string `json:"snapshot_id"`
	FilesNew            int64  `json:"files_new"`
	FilesChanged        int64  `json:"files_changed"`
	FilesUnmodified     int64  `json:"files_unmodified"`
	DataAdded           int64  `json:"data_added"`
	TotalFilesProcessed int64  `json:"total_files_processed"`
	TotalBytesProcessed int64  `json:"total_bytes_processed"`
	// summary (restore)
	FilesRestored int64 `json:"files_restored"`
	FilesSkipped  int64 `json:"files_skipped"`
	BytesRestored int64 `json:"bytes_restored"`
	// error
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	During string `json:"during"`
	Item   string `json:"item"`
	// init
	ID string `json:"id"`
}

func progressOf(m message) Progress {
	return Progress{Percent: m.PercentDone * 100, FilesDone: m.FilesDone, FilesTotal: m.TotalFiles, BytesDone: m.BytesDone, BytesTotal: m.TotalBytes}
}

func (p *repo) Init(ctx context.Context) (string, error) {
	res, err := p.run(ctx, call{op: "init", args: []string{"init", "--json"}})
	if err != nil {
		return "", err
	}
	for _, line := range bytes.Split(res.stdout, []byte("\n")) {
		var m message
		if json.Unmarshal(bytes.TrimSpace(line), &m) == nil && m.ID != "" {
			return m.ID, nil
		}
	}
	cfg, err := p.Config(ctx)
	return cfg.ID, err
}

func (p *repo) Config(ctx context.Context) (Config, error) {
	res, err := p.run(ctx, call{op: "cat config", args: []string{"cat", "config", "--json"}})
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(bytes.TrimSpace(res.stdout), &c); err != nil || c.ID == "" {
		return Config{}, errorf("cat config", CodeFailed, "unexpected output")
	}
	return c, nil
}

func (p *repo) Backup(ctx context.Context, req BackupRequest) (BackupSummary, error) {
	args := []string{"backup", "--json"}
	if req.Host != "" {
		args = append(args, "--host", req.Host)
	}
	for _, t := range req.Tags {
		args = append(args, "--tag", t)
	}
	if !req.Time.IsZero() {
		args = append(args, "--time", req.Time.UTC().Format("2006-01-02 15:04:05"))
	}
	for _, e := range req.Excludes {
		args = append(args, "--exclude", e)
	}
	if req.Stdin != nil {
		name := req.StdinFilename
		if name == "" {
			name = "stdin"
		}
		args = append(args, "--stdin", "--stdin-filename", name)
	} else {
		if len(req.Paths) == 0 {
			return BackupSummary{}, errorf("backup", CodeFailed, "no paths to back up")
		}
		args = append(args, "--")
		args = append(args, req.Paths...)
	}
	var sum BackupSummary
	var found bool
	res, err := p.run(ctx, call{op: "backup", args: args, stdin: req.Stdin, dir: req.Dir, ok: []int{3},
		lines: func(line []byte) {
			var m message
			if json.Unmarshal(line, &m) != nil {
				return
			}
			switch m.MessageType {
			case "status":
				if req.Progress != nil {
					req.Progress(progressOf(m))
				}
			case "error":
				if len(sum.Errors) < maxErrors {
					text := m.Item
					if m.Error != nil {
						text = strings.TrimSpace(m.Item + ": " + m.Error.Message)
					}
					sum.Errors = append(sum.Errors, scrub(text, p.secrets()))
				}
			case "summary":
				found = true
				sum.SnapshotID = m.SnapshotID
				sum.FilesNew, sum.FilesChanged, sum.FilesUnmodified = m.FilesNew, m.FilesChanged, m.FilesUnmodified
				sum.DataAdded, sum.TotalFilesProcessed, sum.TotalBytesProcessed = m.DataAdded, m.TotalFilesProcessed, m.TotalBytesProcessed
			}
		}})
	if err != nil {
		return sum, err
	}
	if !found || sum.SnapshotID == "" {
		return sum, errorf("backup", CodeFailed, "restic reported no snapshot")
	}
	sum.Incomplete = res.exitCode == 3
	return sum, nil
}

func (p *repo) Snapshots(ctx context.Context, f SnapshotFilter) ([]Snapshot, error) {
	args := []string{"snapshots", "--json"}
	if len(f.Tags) > 0 {
		args = append(args, "--tag", strings.Join(f.Tags, ","))
	}
	if f.Host != "" {
		args = append(args, "--host", f.Host)
	}
	res, err := p.run(ctx, call{op: "snapshots", args: args})
	if err != nil {
		return nil, err
	}
	var out []Snapshot
	if err := json.Unmarshal(bytes.TrimSpace(res.stdout), &out); err != nil {
		return nil, errorf("snapshots", CodeFailed, "unexpected output")
	}
	return out, nil
}

func (p *repo) Ls(ctx context.Context, snapshotID, dir string, recursive bool, limit int) (Listing, error) {
	if limit <= 0 {
		limit = DefaultMaxNodes
	}
	args := []string{"ls", "--json"}
	if recursive && dir != "" && dir != "/" {
		args = append(args, "--recursive")
	}
	args = append(args, "--", snapshotID)
	if dir != "" {
		args = append(args, dir)
	}
	var l Listing
	_, err := p.run(ctx, call{op: "ls", args: args, lines: func(line []byte) {
		var n struct {
			Node
			StructType  string `json:"struct_type"`
			MessageType string `json:"message_type"`
		}
		if json.Unmarshal(line, &n) != nil || (n.StructType != "node" && n.MessageType != "node") {
			return
		}
		if len(l.Nodes) >= limit {
			l.Truncated = true
			return
		}
		l.Nodes = append(l.Nodes, n.Node)
	}})
	return l, err
}

func (p *repo) Dump(ctx context.Context, snapshotID, file string, w io.Writer) error {
	_, err := p.run(ctx, call{op: "dump", args: []string{"dump", "--", snapshotID, file}, raw: w})
	return err
}

func (p *repo) Restore(ctx context.Context, req RestoreRequest) (RestoreSummary, error) {
	if req.SnapshotID == "" || req.Target == "" {
		return RestoreSummary{}, errorf("restore", CodeFailed, "snapshot and target are required")
	}
	args := []string{"restore", "--json", "--target", req.Target}
	for _, i := range req.Include {
		args = append(args, "--include", i)
	}
	for _, e := range req.Exclude {
		args = append(args, "--exclude", e)
	}
	if req.Overwrite != "" {
		args = append(args, "--overwrite", req.Overwrite)
	}
	if req.Delete {
		args = append(args, "--delete")
	}
	args = append(args, "--", req.SnapshotID)
	var sum RestoreSummary
	_, err := p.run(ctx, call{op: "restore", args: args, lines: func(line []byte) {
		var m message
		if json.Unmarshal(line, &m) != nil {
			return
		}
		switch m.MessageType {
		case "status":
			if req.Progress != nil {
				req.Progress(progressOf(m))
			}
		case "summary":
			sum = RestoreSummary{TotalFiles: m.TotalFiles, FilesRestored: m.FilesRestored, FilesSkipped: m.FilesSkipped,
				TotalBytes: m.TotalBytes, BytesRestored: m.BytesRestored}
		}
	}})
	return sum, err
}

func (p *repo) Forget(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	args := append([]string{"forget", "--"}, ids...)
	_, err := p.run(ctx, call{op: "forget", args: args})
	return err
}

func (p *repo) Prune(ctx context.Context) error {
	_, err := p.run(ctx, call{op: "prune", args: []string{"prune"}})
	return err
}

func (p *repo) Stats(ctx context.Context) (Stats, error) {
	res, err := p.run(ctx, call{op: "stats", args: []string{"stats", "--json", "--mode", "raw-data"}})
	if err != nil {
		return Stats{}, err
	}
	var s Stats
	if err := json.Unmarshal(bytes.TrimSpace(res.stdout), &s); err != nil {
		return Stats{}, errorf("stats", CodeFailed, "unexpected output")
	}
	return s, nil
}

func (p *repo) Check(ctx context.Context, req CheckRequest) (CheckResult, error) {
	args := []string{"check"}
	if req.ReadDataSubset != "" {
		args = append(args, "--read-data-subset", req.ReadDataSubset)
	}
	_, err := p.run(ctx, call{op: "check", args: args})
	if err != nil {
		var e *Error
		// A failed check that is not a location/key/lock problem means
		// the repository is damaged.
		if errors.As(err, &e) && e.Code == CodeFailed {
			e.Code = CodeRepositoryDamaged
		}
		return CheckResult{}, err
	}
	return CheckResult{ReadData: req.ReadDataSubset != ""}, nil
}

func (p *repo) Keys(ctx context.Context) ([]Key, error) {
	res, err := p.run(ctx, call{op: "key list", args: []string{"key", "list", "--json"}})
	if err != nil {
		return nil, err
	}
	var out []Key
	if err := json.Unmarshal(bytes.TrimSpace(res.stdout), &out); err != nil {
		return nil, errorf("key list", CodeFailed, "unexpected output")
	}
	return out, nil
}

func (p *repo) AddKey(ctx context.Context, newPassword string) error {
	if newPassword == "" {
		return errorf("key add", CodeFailed, "empty key")
	}
	_, err := p.run(ctx, call{op: "key add", args: []string{"key", "add", "--user", "docker-manager", "--host", "docker-manager"}, newPassword: newPassword})
	return err
}

func (p *repo) RemoveKey(ctx context.Context, id string) error {
	_, err := p.run(ctx, call{op: "key remove", args: []string{"key", "remove", "--", id}})
	return err
}

func (p *repo) Unlock(ctx context.Context) error {
	_, err := p.run(ctx, call{op: "unlock", args: []string{"unlock"}})
	return err
}

// fdPath is the path through which a child reads inherited descriptor n.
func fdPath(n int) string { return "/proc/self/fd/" + strconv.Itoa(n) }
