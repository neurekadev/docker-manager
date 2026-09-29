package managermove

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/backup"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authsep"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/backups"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/secrets"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
)

// The new manager's side: in waiting mode it checks in with the old
// manager every WaitInterval (a signed GET that returns the move's state
// and the apps' progress) and asks for the handoff once the move is ready,
// until it gets the package, stores it under
// <data>/move-incoming/waiting/, checks it and stages it as a restore of
// kind move; a controlled restart applies it (finishMove). Its progress is
// GET /api/v1/move/status (WaitStatus).

// Paths and headers of the old manager's move routes.
const (
	CheckInPath = "/api/v1/manager/move/check-in"
	HandoffPath = "/api/v1/manager/move/handoff"
	ConfirmPath = "/api/v1/manager/move/confirm"
	// JobsRunningHeader carries the number of jobs a handoff waits for.
	JobsRunningHeader = "X-Docker-Manager-Jobs-Running"
	// PackageSizeHeader carries the total size of the parts of a handoff.
	PackageSizeHeader = "X-Docker-Manager-Move-Size"
	// MoveStateHeader, MoveStacksHeader ("<moved>/<total>") and
	// MoveCurrentStackHeader carry the progress of a not_ready handoff.
	MoveStateHeader        = "X-Docker-Manager-Move-State"
	MoveStacksHeader       = "X-Docker-Manager-Move-Stacks"
	MoveCurrentStackHeader = "X-Docker-Manager-Move-Current-Stack"
	// PackageContentType is the media type of the encrypted handoff stream.
	PackageContentType = "application/octet-stream"
)

// Waiting mode timing.
const (
	// WaitInterval is how often the waiting manager asks for the handoff.
	WaitInterval = 10 * time.Second
	// waitRefusedInterval is the pace after a refusal (a wrong code, the
	// clocks): only a change on either side helps.
	waitRefusedInterval = time.Minute
	waitRetryMax        = time.Minute
)

// receiveDirName is the incoming directory of waiting mode.
const receiveDirName = "waiting"

// Error classes of waiting mode (the status page's errorCode).
const (
	ClassCodeInvalid        = "move_code_invalid"
	ClassClockSkew          = "move_clock_skew"
	ClassUnreachable        = "manager_move_unreachable"
	ClassRefused            = "manager_move_refused"
	ClassTransferFailed     = "manager_move_transfer_failed"
	ClassStateInvalid       = "manager_move_state_invalid"
	ClassSchemaIncompatible = "manager_move_schema_incompatible"
	ClassMoveNotHandedOff   = "manager_move_not_handed_off"
	ClassStageFailed        = "manager_move_stage_failed"
)

// Waiting phases (WaitStatus.Phase).
const (
	// WaitNone: this manager is not waiting for a move.
	WaitNone = "none"
	// WaitConnecting: asking the old manager (ErrorCode set after a
	// failed attempt; it keeps asking).
	WaitConnecting = "connecting"
	// WaitWaiting: the old manager answered: the apps have not moved yet
	// (OldState open: press Move everything there; moving: progress).
	WaitWaiting = "waiting"
	// WaitFinishingJobs: the old manager is read-only while its jobs finish.
	WaitFinishingJobs = "finishing_jobs"
	// WaitCopying: the state streams in (Bytes of TotalBytes).
	WaitCopying = "copying"
	// WaitChecking and WaitStaging: the copy is verified, then staged.
	WaitChecking = "checking"
	WaitStaging  = "staging"
	// WaitRestarting: the copy is staged; this manager restarts as the
	// instance.
	WaitRestarting = "restarting"
	// WaitFailed: the copy was refused; nothing more happens until the
	// manager restarts (Recovery says what to fix).
	WaitFailed = "failed"
	// WaitComplete: this manager runs the moved instance and the move
	// variables are still set (remove them from .env).
	WaitComplete = "complete"
)

// WaitStatus is the waiting mode's progress (public: never the code or
// any content).
type WaitStatus struct {
	Phase string
	// OldManager is the old manager's address (DOCKER_MANAGER_MOVE_FROM).
	OldManager string
	// OldState is the old move's state from its last answer (open,
	// moving, ready, draining, handed_off).
	OldState     string
	StacksMoved  int
	StacksTotal  int
	CurrentStack string
	JobsRunning  int
	Bytes        int64
	TotalBytes   int64
	// LastContactAt is when the old manager last answered.
	LastContactAt *time.Time
	// ErrorCode and Recovery describe the last failure ("" none).
	ErrorCode string
	Recovery  string
	// PublicURL is the address to point at this server once it runs the
	// instance.
	PublicURL string
	// OldManagerConfirmed (complete): the old manager accepted the
	// confirmation.
	OldManagerConfirmed bool
}

// refusal is a classed failure with recovery guidance.
type refusal struct {
	class, msg, recovery string
}

func (r *refusal) Error() string      { return r.msg }
func (r *refusal) ErrorClass() string { return r.class }
func (r *refusal) Recovery() string   { return r.recovery }

func refuse(class, msg, recovery string) error {
	return &refusal{class: class, msg: msg, recovery: recovery}
}

// retryable marks an answer that may pass on its own.
type retryable struct{ error }

// receiveTarget is the old manager's address and the move of a receive.
type receiveTarget struct {
	SourceURL string
	MoveID    string
}

// receiveTask is one handoff attempt: the target, the code (memory only),
// where the package goes and who hears the progress.
type receiveTask struct {
	in     receiveTarget
	code   string
	dir    string
	report func(WaitStatus)
}

func defaultHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Client{
		// No overall timeout: a large database streams for as long as it
		// takes; a stalled transfer is cut by stallTimeout.
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment, DialContext: dialer.DialContext, ForceAttemptHTTP2: true,
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 30 * time.Second,
			// The old manager copies its database before it answers.
			ResponseHeaderTimeout: 15 * time.Minute, IdleConnTimeout: 90 * time.Second,
		},
		// Never follow redirects: requests go to the configured address
		// only.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// stallTimeout cuts a transfer that received nothing for this long.
const stallTimeout = 2 * time.Minute

// Waiting reports whether this manager waits for a move.
func (s *Service) Waiting() bool { return s.opts.Waiting != nil }

// WaitStatus returns the waiting mode's progress; on a manager that runs
// a moved instance with the move variables still set, WaitComplete.
func (s *Service) WaitStatus(ctx context.Context) (WaitStatus, error) {
	public := ""
	if s.opts.PublicURL != nil {
		public = strings.TrimSuffix(s.opts.PublicURL.String(), "/")
	}
	if s.opts.Waiting != nil {
		s.waitMu.Lock()
		st := s.wait
		s.waitMu.Unlock()
		st.OldManager, st.PublicURL = s.opts.Waiting.From.String(), public
		return st, nil
	}
	if s.opts.MoveVariablesSet {
		a, found, err := store.LatestArrivedManagerMove(ctx, s.db)
		if err != nil {
			return WaitStatus{}, err
		}
		if found {
			return WaitStatus{Phase: WaitComplete, PublicURL: public, OldManagerConfirmed: a.ConfirmedAt != nil}, nil
		}
	}
	return WaitStatus{Phase: WaitNone}, nil
}

func (s *Service) setWait(fn func(w *WaitStatus)) {
	s.waitMu.Lock()
	defer s.waitMu.Unlock()
	fn(&s.wait)
}

func (s *Service) receiveDir() string {
	return filepath.Join(s.opts.DataDir, incomingDirName, receiveDirName)
}

// runWaiting asks the old manager for the handoff until it staged the
// copy (then it requests the restart) or the copy was refused.
func (s *Service) runWaiting(ctx context.Context) {
	w := s.opts.Waiting
	moveID, _, ok := authsep.ParseMoveCode(w.Code)
	if !ok {
		s.setWait(func(st *WaitStatus) {
			st.Phase, st.ErrorCode = WaitFailed, ClassCodeInvalid
			st.Recovery = "DOCKER_MANAGER_MOVE_CODE is not a move code: copy the .env shown on the old Docker Manager again, then run docker compose up -d."
		})
		return
	}
	t := receiveTask{in: receiveTarget{SourceURL: strings.TrimSuffix(w.From.String(), "/"), MoveID: moveID}, code: w.Code, dir: s.receiveDir(),
		report: func(ev WaitStatus) { s.setWait(func(st *WaitStatus) { mergeProgress(st, ev) }) }}
	s.log.Warn("waiting for the move: asking the old manager for the handoff", "from", w.From.Host, "move_id", moveID)
	failures := 0
	for {
		wait, stop := s.waitOnce(ctx, t, &failures)
		if stop || ctx.Err() != nil {
			return
		}
		if err := s.sleep(ctx, wait); err != nil {
			return
		}
	}
}

// mergeProgress copies a phase report into the status (clearing the
// error once the old manager answered).
func mergeProgress(st *WaitStatus, ev WaitStatus) {
	st.Phase = ev.Phase
	switch ev.Phase {
	case WaitCopying:
		st.Bytes, st.TotalBytes = ev.Bytes, ev.TotalBytes
	}
}

// waitOnce makes one handoff attempt and, when it streamed a package,
// checks and stages it. It returns how long to wait before the next
// attempt, or stop when nothing more can happen.
func (s *Service) waitOnce(ctx context.Context, t receiveTask, failures *int) (time.Duration, bool) {
	// A staged copy moved t.dir to restore-pending; anything else left
	// there is removed.
	defer func() { _ = os.RemoveAll(t.dir) }()
	if err := os.RemoveAll(t.dir); err != nil {
		s.log.Error("could not clear the incoming move directory", "error", err)
		return waitRetryMax, false
	}
	if err := os.MkdirAll(t.dir, 0o700); err != nil {
		s.log.Error("could not create the incoming move directory", "error", err)
		return waitRetryMax, false
	}
	// The check-in (a signed GET, unaudited) says whether the handoff can
	// come; only a ready (or draining, handed-off) move is asked for it.
	var man Manifest
	ci, err := s.checkInOnce(ctx, t)
	if err == nil {
		switch ci.State {
		case domain.MoveOpen, domain.MoveMoving:
			err = &domain.MoveNotReadyError{State: ci.State, StacksMoved: ci.StacksMoved, StacksTotal: ci.StacksTotal,
				CurrentStack: ci.CurrentStack, RetryAfter: WaitInterval}
		case domain.MoveConfirmed:
			err = errMoveConfirmed
		default:
			man, err = s.handoffOnce(ctx, t)
		}
	}
	if ctx.Err() != nil {
		return 0, true
	}
	now := s.now()
	var nr *domain.MoveNotReadyError
	var jr *domain.JobsRunningError
	var rt *retryable
	var rf *refusal
	switch {
	case err == nil:
		*failures = 0
		return s.finishReceive(ctx, t, man)
	case errors.As(err, &nr):
		*failures = 0
		s.setWait(func(st *WaitStatus) {
			st.Phase, st.OldState, st.StacksMoved, st.StacksTotal, st.CurrentStack = WaitWaiting, string(nr.State), nr.StacksMoved,
				nr.StacksTotal, nr.CurrentStack
			st.JobsRunning, st.LastContactAt, st.ErrorCode, st.Recovery = 0, &now, "", ""
		})
		return retryAfter(nr.RetryAfter), false
	case errors.As(err, &jr):
		*failures = 0
		s.setWait(func(st *WaitStatus) {
			st.Phase, st.OldState, st.JobsRunning, st.LastContactAt, st.ErrorCode, st.Recovery = WaitFinishingJobs,
				string(domain.MoveDraining), jr.Count, &now, "", ""
		})
		return retryAfter(jr.RetryAfter), false
	case errors.Is(err, errPackage):
		*failures++
		s.log.Warn("the handoff transfer failed; asking again", "attempt", *failures, "error", err)
		s.setWait(func(st *WaitStatus) {
			st.Phase, st.LastContactAt, st.ErrorCode = WaitConnecting, &now, ClassTransferFailed
			st.Recovery = "The copy broke off; Docker Manager asks again. If it keeps failing, check the network between the two servers."
		})
		return min(WaitInterval*time.Duration(*failures), waitRetryMax), false
	case errors.As(err, &rt):
		*failures++
		s.log.Warn("the old manager did not answer; asking again", "attempt", *failures, "error", err)
		s.setWait(func(st *WaitStatus) {
			st.Phase, st.ErrorCode = WaitConnecting, ClassUnreachable
			st.Recovery = "The old Docker Manager cannot be reached at DOCKER_MANAGER_MOVE_FROM. Check that it runs, that its port 8080 is " +
				"reachable from this server and that the address in .env is right. Docker Manager keeps asking."
		})
		return min(WaitInterval*time.Duration(*failures), waitRetryMax), false
	case errors.As(err, &rf):
		s.log.Warn("the old manager refused the handoff; asking again later", "class", rf.class)
		s.setWait(func(st *WaitStatus) {
			st.Phase, st.ErrorCode, st.Recovery = WaitConnecting, rf.class, rf.recovery
			if rf.class != ClassUnreachable {
				st.LastContactAt = &now
			}
		})
		return waitRefusedInterval, false
	}
	s.log.Error("the handoff failed; asking again", "error", err)
	s.setWait(func(st *WaitStatus) {
		st.Phase, st.ErrorCode, st.Recovery = WaitConnecting, ClassUnreachable, "Docker Manager asks the old manager again."
	})
	return waitRetryMax, false
}

func retryAfter(d time.Duration) time.Duration {
	if d <= 0 {
		return WaitInterval
	}
	return min(max(d, time.Second), waitRetryMax)
}

// finishReceive checks and stages a received package and requests the
// restart. A refused copy stops waiting mode (a transfer error asks
// again).
func (s *Service) finishReceive(ctx context.Context, t receiveTask, man Manifest) (time.Duration, bool) {
	s.setWait(func(st *WaitStatus) { st.Phase, st.ErrorCode, st.Recovery = WaitChecking, "", "" })
	out, err := s.receiveVerify(ctx, t, receiveOutput{Bytes: man.Size()})
	if err == nil {
		s.setWait(func(st *WaitStatus) { st.Phase = WaitStaging })
		out, err = s.receiveStage(ctx, t, out)
	}
	if err != nil {
		var rf *refusal
		if errors.As(err, &rf) && rf.class == ClassTransferFailed {
			s.setWait(func(st *WaitStatus) { st.Phase, st.ErrorCode, st.Recovery = WaitConnecting, rf.class, rf.recovery })
			return WaitInterval, false
		}
		class, recovery := ClassStageFailed, "Restart Docker Manager on this server (docker compose up -d --force-recreate) to try again."
		if errors.As(err, &rf) {
			class, recovery = rf.class, rf.recovery
		}
		s.log.Error("the moved state was refused; waiting mode stopped", "class", class, "error", err)
		s.setWait(func(st *WaitStatus) { st.Phase, st.ErrorCode, st.Recovery = WaitFailed, class, recovery })
		return 0, true
	}
	s.setWait(func(st *WaitStatus) { st.Phase = WaitRestarting })
	s.log.Warn("the moved manager state is staged; restarting to run it", "move_id", t.in.MoveID, "bytes", out.Bytes)
	if s.opts.RequestRestart != nil {
		s.opts.RequestRestart()
	}
	return 0, true
}

func (s *Service) sleep(ctx context.Context, d time.Duration) error {
	t := s.opts.Clock.NewTimer(max(d, time.Millisecond))
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C():
		return nil
	}
}

// handoffOnce makes one signed handoff request and stores the decrypted
// package in t.dir, checked against the manifest. Failures that may pass
// on their own are *retryable; answers of the old manager are
// *domain.MoveNotReadyError, *domain.JobsRunningError or *refusal.
func (s *Service) handoffOnce(ctx context.Context, t receiveTask) (Manifest, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.in.SourceURL+HandoffPath, nil)
	if err != nil {
		return Manifest{}, err
	}
	auth, err := SignRequest(t.code, http.MethodPost, HandoffPath, s.opts.Clock.Now())
	if err != nil {
		return Manifest{}, err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", PackageContentType+", application/problem+json, application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return Manifest{}, &retryable{errors.New(redactURLError(err))}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Manifest{}, handoffRefusal(resp)
	}
	total, _ := strconv.ParseInt(resp.Header.Get(PackageSizeHeader), 10, 64)
	if t.report != nil {
		t.report(WaitStatus{Phase: WaitCopying, TotalBytes: total})
	}
	key, err := packageKey(t.code, t.in.MoveID)
	if err != nil {
		return Manifest{}, err
	}
	body := newStallReader(resp.Body, stallTimeout, cancel)
	defer body.stop()
	plain, err := newOpenReader(body, key)
	if err != nil {
		return Manifest{}, err
	}
	return readPackage(plain, t.dir, func(n int64) {
		if t.report != nil {
			t.report(WaitStatus{Phase: WaitCopying, Bytes: n, TotalBytes: total})
		}
	})
}

// errMoveConfirmed: the old manager's move is already confirmed.
var errMoveConfirmed = refuse(ClassStateInvalid, "the old manager's move is already confirmed",
	"The move already finished: sign in on the manager that runs Docker Manager now.")

// checkInBody is the old manager's check-in answer (the fields used here).
type checkInBody struct {
	State        string `json:"state"`
	StacksMoved  int    `json:"stacksMoved"`
	StacksTotal  int    `json:"stacksTotal"`
	CurrentStack string `json:"currentStack"`
	JobsRunning  int    `json:"jobsRunning"`
}

// checkInOnce makes one signed check-in. Failures are classified like the
// handoff's.
func (s *Service) checkInOnce(ctx context.Context, t receiveTask) (CheckIn, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.in.SourceURL+CheckInPath, nil)
	if err != nil {
		return CheckIn{}, err
	}
	auth, err := SignRequest(t.code, http.MethodGet, CheckInPath, s.opts.Clock.Now())
	if err != nil {
		return CheckIn{}, err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", "application/json, application/problem+json")
	resp, err := s.http.Do(req)
	if err != nil {
		return CheckIn{}, &retryable{errors.New(redactURLError(err))}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return CheckIn{}, handoffRefusal(resp)
	}
	var b checkInBody
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&b); err != nil || b.State == "" {
		return CheckIn{}, &retryable{errors.New("the old manager's check-in answer is unreadable")}
	}
	return CheckIn{State: domain.ManagerMoveState(b.State), StacksMoved: b.StacksMoved, StacksTotal: b.StacksTotal,
		CurrentStack: b.CurrentStack, JobsRunning: b.JobsRunning}, nil
}

// apiError is the old manager's error body (the fields used here).
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// handoffRefusal classifies a handoff answer other than 200.
func handoffRefusal(resp *http.Response) error {
	var e apiError
	_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e)
	ra, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
	switch {
	case resp.StatusCode == http.StatusConflict && e.Code == "manager_move_not_ready":
		nr := &domain.MoveNotReadyError{State: domain.ManagerMoveState(resp.Header.Get(MoveStateHeader)),
			CurrentStack: resp.Header.Get(MoveCurrentStackHeader), RetryAfter: time.Duration(ra) * time.Second}
		if moved, total, ok := strings.Cut(resp.Header.Get(MoveStacksHeader), "/"); ok {
			nr.StacksMoved, _ = strconv.Atoi(moved)
			nr.StacksTotal, _ = strconv.Atoi(total)
		}
		return nr
	case resp.StatusCode == http.StatusConflict && e.Code == "jobs_running":
		n, _ := strconv.Atoi(resp.Header.Get(JobsRunningHeader))
		return &domain.JobsRunningError{Count: max(n, 1), RetryAfter: time.Duration(ra) * time.Second}
	case resp.StatusCode == http.StatusUnauthorized && e.Code == "move_clock_skew":
		return refuse(ClassClockSkew, "the clocks of the two servers differ by more than five minutes",
			"Set the clock of both servers right (turn on time synchronization, NTP). Docker Manager keeps asking.")
	case resp.StatusCode == http.StatusUnauthorized:
		return refuse(ClassCodeInvalid, "the old manager did not accept the move code",
			"Check DOCKER_MANAGER_MOVE_CODE in .env: it must be the code of the move shown on the old Docker Manager (the code of a "+
				"cancelled or expired move no longer works). Fix it, then run docker compose up -d.")
	case resp.StatusCode == http.StatusConflict && e.Code == "manager_move_state":
		return errMoveConfirmed
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed:
		return refuse(ClassRefused, "the address does not answer as a Docker Manager that can hand itself over",
			"Check DOCKER_MANAGER_MOVE_FROM in .env (http://<old server>:8080), and update the old Docker Manager to a version that "+
				"can move to a new server.")
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return &retryable{fmt.Errorf("the old manager answered %d", resp.StatusCode)}
	}
	return refuse(ClassRefused, fmt.Sprintf("the old manager answered %d %s", resp.StatusCode, safeMessage(e.Message)),
		"Check the old Docker Manager and DOCKER_MANAGER_MOVE_FROM. Docker Manager keeps asking.")
}

// safeMessage bounds a message from the old manager (API errors never
// carry secrets).
func safeMessage(m string) string {
	if len(m) > 300 {
		m = m[:300]
	}
	return m
}

// redactURLError keeps a transport error's operation, URL and cause
// (the URL has no query or credentials).
func redactURLError(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Op + " " + ue.URL + ": " + ue.Err.Error()
	}
	return err.Error()
}

// stallReader cancels a transfer that receives nothing for d.
type stallReader struct {
	r     io.Reader
	timer *time.Timer
	d     time.Duration
}

func newStallReader(r io.Reader, d time.Duration, cancel context.CancelFunc) *stallReader {
	return &stallReader{r: r, d: d, timer: time.AfterFunc(d, cancel)}
}

func (s *stallReader) Read(b []byte) (int, error) {
	n, err := s.r.Read(b)
	if n > 0 {
		s.timer.Reset(s.d)
	}
	return n, err
}

func (s *stallReader) stop() { s.timer.Stop() }

// receiveKeyFile holds the opened secret key between verify and stage.
const receiveKeyFile = "secret.key"

// receiveOutput is what the checks recorded for the stage.
type receiveOutput struct {
	Bytes          int64
	InstanceID     string
	Generation     int64
	AppVersion     string
	SchemaLatest   string
	Verified       bool
	RestartPending bool
}

// receiveVerify checks the copy in t.dir: the sealed key opens with the
// code, the database is intact and belongs to state.json's instance and
// generation, its schema is one this build knows, the key opens its sealed
// settings and the move's row arrived. The opened key is kept in t.dir for
// the stage.
func (s *Service) receiveVerify(ctx context.Context, t receiveTask, out receiveOutput) (receiveOutput, error) {
	info, key, err := s.verifyPackage(ctx, t.dir, t.in, t.code)
	if err != nil {
		return out, err
	}
	if err := secrets.ReplaceKeyFile(filepath.Join(t.dir, receiveKeyFile), key); err != nil {
		return out, err
	}
	out.InstanceID, out.Generation, out.AppVersion, out.SchemaLatest, out.Verified = info.InstanceID, info.Generation, info.App.Version,
		info.Schema.Latest(), true
	return out, nil
}

func stateMissing(msg string) error {
	return refuse(ClassTransferFailed, msg, "Docker Manager asks the old manager again; it sends the same copy.")
}

// verifyPackage runs the checks of receiveVerify on dir.
func (s *Service) verifyPackage(ctx context.Context, dir string, in receiveTarget, code string) (StateInfo, secrets.Key, error) {
	raw, err := os.ReadFile(filepath.Join(dir, PartState)) //nolint:gosec // below the data directory
	if err != nil {
		return StateInfo{}, secrets.Key{}, stateMissing("the received state.json is missing")
	}
	var info StateInfo
	if json.Unmarshal(raw, &info) != nil || info.Format != PackageFormat {
		return StateInfo{}, secrets.Key{}, stateMissing("the received state.json is unreadable")
	}
	if info.Version > PackageVersion {
		return StateInfo{}, secrets.Key{}, refuse(ClassSchemaIncompatible, "the old manager ("+info.App.Version+") is newer than this one",
			"Update Docker Manager on this server to at least "+info.App.Version+" (docker compose pull, docker compose up -d).")
	}
	if info.MoveID != in.MoveID {
		return StateInfo{}, secrets.Key{}, refuse(ClassCodeInvalid, "the received state belongs to another move",
			"Check DOCKER_MANAGER_MOVE_CODE in .env: it must be the code shown on the old Docker Manager.")
	}
	sealed, err := os.ReadFile(filepath.Join(dir, PartSealedKey)) //nolint:gosec // below the data directory
	if err != nil {
		return StateInfo{}, secrets.Key{}, stateMissing("the received secret key is missing")
	}
	key, err := openSecretKey(sealed, code, info.InstanceID)
	if err != nil || key.ID() != info.SecretKeyID {
		return StateInfo{}, secrets.Key{}, refuse(ClassCodeInvalid, "the received secret key does not open with this move code",
			"Check DOCKER_MANAGER_MOVE_CODE in .env: it must be the code shown on the old Docker Manager.")
	}
	if err := s.checkCopy(ctx, filepath.Join(dir, PartDatabase), info, key); err != nil {
		return StateInfo{}, secrets.Key{}, err
	}
	return info, key, nil
}

// checkCopy opens the database copy (checkRestoredDatabase's checks for
// a move).
func (s *Service) checkCopy(ctx context.Context, path string, info StateInfo, key secrets.Key) error {
	db, err := store.Open(ctx, path)
	if err != nil {
		return stateMissing("the received database cannot be opened")
	}
	defer func() { _ = db.Close() }()
	var check string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&check); err != nil || check != "ok" {
		return stateMissing("the received database fails its integrity check")
	}
	inst, found, err := store.GetInstance(ctx, db)
	if err != nil || !found || inst.ID != info.InstanceID || inst.Generation != info.Generation {
		return refuse(ClassStateInvalid, "the received database does not match its state.json (instance or generation)",
			"The old manager's state looks damaged. On the old Docker Manager, cancel the move and resume there, then check its database.")
	}
	if s.opts.CheckSchema != nil {
		if err := s.opts.CheckSchema(ctx, db); err != nil {
			if errors.Is(err, store.ErrUnknownMigrations) {
				return refuse(ClassSchemaIncompatible, "the old manager ("+info.App.Version+") has a newer database than this Docker Manager knows",
					"Update Docker Manager on this server to at least "+info.App.Version+" (docker compose pull, docker compose up -d).")
			}
			return err
		}
	}
	if err := backups.CheckSealedSettings(ctx, db, key); err != nil {
		if errors.Is(err, backups.ErrSealedSettings) {
			return refuse(ClassStateInvalid, "the received secret key does not decrypt the received settings",
				"The old manager's secret key does not match its database. On the old Docker Manager, cancel the move and resume there.")
		}
		return err
	}
	m, err := store.GetManagerMove(ctx, db, info.MoveID)
	if err != nil || m.State != domain.MoveArrived {
		return refuse(ClassMoveNotHandedOff, "the received database does not record this move as handed off",
			"Check DOCKER_MANAGER_MOVE_CODE in .env: it must be the code shown on the old Docker Manager.")
	}
	return nil
}

// receiveStage stages the verified copy in t.dir as a restore of kind move
// into restore-pending (the marker carries the move code sealed with the
// moved secret key); the next start applies it.
func (s *Service) receiveStage(_ context.Context, t receiveTask, out receiveOutput) (receiveOutput, error) {
	if !out.Verified {
		return out, errors.New("the copy was not verified")
	}
	key, err := secrets.LoadKeyFile(filepath.Join(t.dir, receiveKeyFile))
	if err != nil {
		return out, errors.New("the received secret key is missing")
	}
	sealedCode, err := secrets.NewKeyring(key).Seal([]byte(t.code), SealContext(t.in.MoveID))
	if err != nil {
		return out, err
	}
	drafts := filepath.Join(t.dir, PartTemplates)
	if _, err := os.Stat(drafts); err != nil {
		drafts = ""
	}
	mk := backups.RestoreMarker{Format: backups.RestoreMarkerFormat, Version: 1, Kind: backups.RestoreKindMove,
		InstanceID: out.InstanceID, App: backup.AppInfo{Version: out.AppVersion}, SchemaLatest: out.SchemaLatest, Manifests: []backup.Manifest{},
		StagedAt: s.now(), MoveID: t.in.MoveID, SourceURL: t.in.SourceURL, SealedMoveCode: sealedCode, Generation: out.Generation}
	if err := backups.StageRestore(s.opts.DataDir, t.dir, filepath.Join(t.dir, PartDatabase), drafts, key, mk); err != nil {
		return out, err
	}
	out.RestartPending = true
	return out, nil
}
