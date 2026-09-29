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

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/backup"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobexec"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/audit"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authsep"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/backups"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/jobs"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/secrets"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
)

// The new manager's side: manager.receive (steps handoff, verify, stage)
// pulls the package from the old manager into <data>/move-incoming/<job>/,
// checks it and stages it as a restore of kind move; a controlled restart
// applies it.

const receiveKind = jobspec.ManagerReceive

// Paths of the old manager's move routes.
const (
	HandoffPath = "/api/v1/manager/move/handoff"
	ConfirmPath = "/api/v1/manager/move/confirm"
	// JobsRunningHeader carries the number of jobs a handoff waits for.
	JobsRunningHeader = "X-Docker-Manager-Jobs-Running"
)

// receiveKeyFile holds the opened secret key between verify and stage.
const receiveKeyFile = "secret.key"

// Job error classes of manager.receive (setup status errorCode).
const (
	ClassCodeInvalid        = "move_code_invalid"
	ClassUnreachable        = "manager_move_unreachable"
	ClassRefused            = "manager_move_refused"
	ClassJobsRunning        = "manager_move_jobs_running"
	ClassTransferFailed     = "manager_move_transfer_failed"
	ClassStateInvalid       = "manager_move_state_invalid"
	ClassSchemaIncompatible = "manager_move_schema_incompatible"
	ClassMoveNotHandedOff   = "manager_move_not_handed_off"
	ClassSecretsLost        = "manager_move_code_lost"
	receiveTransferAttempts = 5
	receiveRetryMin         = 5 * time.Second
	receiveRetryMax         = time.Minute
)

// refusal is a classed step failure with recovery guidance.
type refusal struct {
	class, msg, recovery string
}

func (r *refusal) Error() string      { return r.msg }
func (r *refusal) ErrorClass() string { return r.class }
func (r *refusal) Recovery() string   { return r.recovery }

func refuse(class, msg, recovery string) error {
	return &refusal{class: class, msg: msg, recovery: recovery}
}

var errCodeLost = refuse(ClassSecretsLost, "the move code was lost (this manager restarted)",
	"Start the move again with the same code: Docker Manager keeps it in memory only.")

// receiveInput is manager.receive's input (never the code).
type receiveInput struct {
	SourceURL string `json:"sourceUrl"`
	MoveID    string `json:"moveId"`
}

// receiveOutput is what the steps recorded.
type receiveOutput struct {
	Bytes          int64  `json:"bytes,omitempty"`
	InstanceID     string `json:"instanceId,omitempty"`
	Generation     int64  `json:"generation,omitempty"`
	AppVersion     string `json:"appVersion,omitempty"`
	SchemaLatest   string `json:"schemaLatest,omitempty"`
	Verified       bool   `json:"verified,omitempty"`
	RestartPending bool   `json:"restartPending,omitempty"`
}

// receiveProgress is the live progress of a receive (setup status).
type receiveProgress struct {
	WaitingJobs int
	Bytes       int64
	TotalBytes  int64
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
		// Never follow redirects: the code is sent to the address the
		// owner entered only.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// stallTimeout cuts a transfer that received nothing for this long.
const stallTimeout = 2 * time.Minute

// NormalizeSource checks the old manager's address: an https origin
// (no credentials, path, query or fragment).
func NormalizeSource(raw string) (*url.URL, error) {
	bad := func(msg string) error { return &domain.FieldError{Field: "sourceUrl", Message: msg} }
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return nil, bad("enter the old Docker Manager's address, like https://docker.example.com")
	}
	if u.Scheme != "https" {
		return nil, bad("the old Docker Manager must be reached over HTTPS")
	}
	if u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, bad("enter only the address (scheme and host), like https://docker.example.com")
	}
	return &url.URL{Scheme: "https", Host: strings.ToLower(u.Host)}, nil
}

// StartReceive queues manager.receive (first-run setup only; the caller
// checked that setup is open). The code stays in memory for the job.
func (s *Service) StartReceive(ctx context.Context, sourceURL, code string) (domain.Job, error) {
	u, err := NormalizeSource(sourceURL)
	if err != nil {
		return domain.Job{}, err
	}
	moveID, _, ok := authsep.ParseMoveCode(strings.TrimSpace(code))
	if !ok {
		return domain.Job{}, &domain.FieldError{Field: "code", Message: "this is not a move code (it starts with dmm_)"}
	}
	code = strings.TrimSpace(code)
	if s.opts.Jobs == nil {
		return domain.Job{}, jobs.ErrManagerMoved
	}
	s.recvMu.Lock()
	defer s.recvMu.Unlock()
	busy, err := s.receiveOrImportRunning(ctx)
	if err != nil {
		return domain.Job{}, err
	}
	if busy || backups.RestorePending(s.opts.DataDir) {
		return domain.Job{}, domain.ErrManagerMoveInProgress
	}
	j, _, err := s.opts.Jobs.Enqueue(ctx, jobs.Request{Kind: receiveKind, Principal: authz.Service(),
		Targets: []domain.JobTarget{{Type: domain.TargetManager, ID: "instance"}},
		Input:   receiveInput{SourceURL: u.String(), MoveID: moveID}})
	if err != nil {
		return domain.Job{}, err
	}
	s.codes[j.ID] = code
	s.progress[j.ID] = &receiveProgress{}
	audit.SetDetail(ctx, "sourceHost", u.Host)
	audit.SetDetail(ctx, "moveId", moveID)
	return j, nil
}

func (s *Service) receiveOrImportRunning(ctx context.Context) (bool, error) {
	list, err := s.opts.Jobs.List(ctx, domain.JobFilter{Kinds: []domain.JobKind{receiveKind, jobspec.BackupImport},
		States: []domain.JobState{domain.JobQueued, domain.JobBlocked, domain.JobDispatched, domain.JobRunning, domain.JobCancelling}, Limit: 1})
	return len(list) > 0, err
}

// ReceiveStatus is the newest receive's progress (setup status).
type ReceiveStatus struct {
	JobID       string
	State       domain.JobState
	Step        string
	WaitingJobs int
	Bytes       int64
	TotalBytes  int64
	ErrorClass  string
	// Recovery is the failure's guidance (never secrets).
	Recovery       string
	RestartPending bool
}

// LatestReceive returns the newest receive's state (nil when none).
func (s *Service) LatestReceive(ctx context.Context) (*ReceiveStatus, error) {
	if s.opts.Jobs == nil {
		return nil, nil
	}
	list, err := s.opts.Jobs.List(ctx, domain.JobFilter{Kinds: []domain.JobKind{receiveKind}, Limit: 1})
	if err != nil || len(list) == 0 {
		return nil, err
	}
	j := list[0]
	st := &ReceiveStatus{JobID: j.ID, State: j.State, Step: j.CurrentStep, ErrorClass: j.ErrorClass, Recovery: j.Recovery,
		RestartPending: backups.RestorePending(s.opts.DataDir)}
	s.recvMu.Lock()
	if p, ok := s.progress[j.ID]; ok {
		st.WaitingJobs, st.Bytes, st.TotalBytes = p.WaitingJobs, p.Bytes, p.TotalBytes
	}
	s.recvMu.Unlock()
	return st, nil
}

func (s *Service) setProgress(jobID string, fn func(p *receiveProgress)) {
	s.recvMu.Lock()
	defer s.recvMu.Unlock()
	p, ok := s.progress[jobID]
	if !ok {
		p = &receiveProgress{}
		s.progress[jobID] = p
	}
	fn(p)
}

func (s *Service) codeFor(jobID string) (string, bool) {
	s.recvMu.Lock()
	defer s.recvMu.Unlock()
	c, ok := s.codes[jobID]
	return c, ok
}

func (s *Service) incomingDir(jobID string) string {
	return filepath.Join(s.opts.DataDir, incomingDirName, jobID)
}

func (s *Service) receiveExecutor() jobexec.Executor {
	return jobexec.Executor{Kind: receiveKind, Steps: map[string]jobexec.StepFunc{
		"handoff": s.stepHandoff,
		"verify":  s.stepVerify,
		"stage":   s.stepStage,
	}}
}

func (s *Service) receiveJob(sc *jobexec.StepContext) (receiveInput, string, error) {
	var in receiveInput
	if err := json.Unmarshal(sc.Input, &in); err != nil {
		return in, "", err
	}
	code, ok := s.codeFor(sc.JobID)
	if !ok {
		return in, "", errCodeLost
	}
	return in, code, nil
}

// stepHandoff calls the old manager's handoff until it streams the
// package (retrying while its jobs run, up to ReceiveWait, and after a
// broken transfer) and stores it, checked against the manifest.
func (s *Service) stepHandoff(ctx context.Context, sc *jobexec.StepContext) error {
	in, code, err := s.receiveJob(sc)
	if err != nil {
		return err
	}
	dir := s.incomingDir(sc.JobID)
	start := s.opts.Clock.Now()
	transferFailures, otherFailures := 0, 0
	for {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		sc.Progress(ctx, 5, "asking the old manager for the handoff")
		man, err := s.handoffOnce(ctx, sc, in.SourceURL, code, dir)
		if err == nil {
			s.setProgress(sc.JobID, func(p *receiveProgress) { p.WaitingJobs = 0 })
			return sc.SetOutput(ctx, receiveOutput{Bytes: man.Size()})
		}
		var jr *domain.JobsRunningError
		var rt *retryable
		var pkgErr bool
		switch {
		case errors.As(err, &jr):
			if s.opts.Clock.Now().Sub(start) >= ReceiveWait {
				return refuse(ClassJobsRunning, fmt.Sprintf("%d jobs were still running on the old manager after %s", jr.Count, ReceiveWait),
					"Let the old manager's jobs finish (or cancel them there after cancelling the move), then start the move again.")
			}
			s.setProgress(sc.JobID, func(p *receiveProgress) { p.WaitingJobs = jr.Count })
			sc.Progress(ctx, 5, fmt.Sprintf("waiting for %d jobs to finish on the old manager", jr.Count))
		case errors.Is(err, errPackage):
			pkgErr = true
			transferFailures++
			if transferFailures >= receiveTransferAttempts {
				return refuse(ClassTransferFailed, err.Error(), "Check the network between the two servers, then start the move again with the same code.")
			}
			s.log.Warn("the handoff transfer failed; retrying", "job_id", sc.JobID, "attempt", transferFailures, "error", err)
		case errors.As(err, &rt):
			otherFailures++
			if otherFailures >= receiveTransferAttempts {
				return refuse(ClassUnreachable, "the old manager could not be reached: "+err.Error(),
					"Check the address (the old Docker Manager's public HTTPS address) and that it is running, then start the move again.")
			}
			s.log.Warn("the old manager did not answer the handoff; retrying", "job_id", sc.JobID, "error", err)
		default:
			return err
		}
		wait := receiveRetryMin
		if jr != nil && jr.RetryAfter > 0 {
			wait = min(max(jr.RetryAfter, time.Second), receiveRetryMax)
		} else if !pkgErr {
			wait = min(receiveRetryMin*time.Duration(otherFailures), receiveRetryMax)
		}
		if err := s.sleep(ctx, wait); err != nil {
			return err
		}
	}
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

// handoffOnce makes one handoff request. Failures that may pass on their
// own are *retryable.
func (s *Service) handoffOnce(ctx context.Context, sc *jobexec.StepContext, source, code, dir string) (Manifest, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(source, "/")+HandoffPath, nil)
	if err != nil {
		return Manifest{}, err
	}
	req.Header.Set("Authorization", "Bearer "+code)
	req.Header.Set("Accept", PackageContentType+", application/problem+json, application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return Manifest{}, &retryable{errors.New(redactURLError(err))}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Manifest{}, s.handoffRefusal(resp)
	}
	total, _ := strconv.ParseInt(resp.Header.Get(packageSizeHeader), 10, 64)
	s.setProgress(sc.JobID, func(p *receiveProgress) { p.WaitingJobs, p.TotalBytes, p.Bytes = 0, total, 0 })
	body := newStallReader(resp.Body, stallTimeout, cancel)
	defer body.stop()
	lastPercent := -1
	man, err := readPackage(body, dir, func(n int64) {
		s.setProgress(sc.JobID, func(p *receiveProgress) { p.Bytes = n })
		if total > 0 {
			if pct := 10 + int(60*n/total); pct != lastPercent {
				lastPercent = pct
				sc.Progress(ctx, min(pct, 70), "receiving the manager state")
			}
		}
	})
	return man, err
}

// packageSizeHeader carries the total size of the parts of a handoff.
const packageSizeHeader = "X-Docker-Manager-Move-Size"

// apiError is the old manager's error body (the fields used here).
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (s *Service) handoffRefusal(resp *http.Response) error {
	var e apiError
	_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e)
	switch {
	case resp.StatusCode == http.StatusConflict && e.Code == "jobs_running":
		n, _ := strconv.Atoi(resp.Header.Get(JobsRunningHeader))
		ra, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
		return &domain.JobsRunningError{Count: max(n, 1), RetryAfter: time.Duration(ra) * time.Second}
	case resp.StatusCode == http.StatusUnauthorized:
		return refuse(ClassCodeInvalid, "the old manager did not accept the move code",
			"The code is wrong, expired (one hour), cancelled or already used: create a new move code on the old manager and start again.")
	case resp.StatusCode == http.StatusConflict && e.Code == "manager_move_state":
		return refuse(ClassStateInvalid, "the old manager's move is already confirmed",
			"The move already finished: sign in on the manager that runs the instance.")
	case resp.StatusCode == http.StatusForbidden:
		return refuse(ClassRefused, "the old manager refused the handoff: "+safeMessage(e.Message),
			"The request must reach the old Docker Manager over HTTPS on its public address; check the address and its reverse proxy.")
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed:
		return refuse(ClassRefused, "the address does not answer as a Docker Manager that can hand off its state",
			"Enter the old Docker Manager's public address, and update it to a version that can move to a new server.")
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return &retryable{fmt.Errorf("the old manager answered %d", resp.StatusCode)}
	}
	return refuse(ClassRefused, fmt.Sprintf("the old manager answered %d %s", resp.StatusCode, safeMessage(e.Message)),
		"Check the old Docker Manager, then start the move again.")
}

// retryable marks an answer that may pass on its own.
type retryable struct{ error }

// safeMessage bounds a message from the old manager (it never carries
// secrets: API errors are safe to show).
func safeMessage(m string) string {
	if len(m) > 300 {
		m = m[:300]
	}
	return m
}

// redactURLError drops the request URL's query (there is none; defensive)
// from transport errors.
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

// stepVerify checks the received copy: the sealed key opens with the
// code, the database is intact and belongs to state.json's instance and
// generation, its schema is one this build knows, the key opens its sealed
// settings and the move's row arrived.
func (s *Service) stepVerify(ctx context.Context, sc *jobexec.StepContext) error {
	in, code, err := s.receiveJob(sc)
	if err != nil {
		return err
	}
	var out receiveOutput
	_ = json.Unmarshal(sc.Output(), &out)
	dir := s.incomingDir(sc.JobID)
	sc.Progress(ctx, 75, "checking the received state")
	info, key, err := s.verifyPackage(ctx, dir, in, code)
	if err != nil {
		return err
	}
	if err := secrets.ReplaceKeyFile(filepath.Join(dir, receiveKeyFile), key); err != nil {
		return err
	}
	out.InstanceID, out.Generation, out.AppVersion, out.SchemaLatest, out.Verified = info.InstanceID, info.Generation, info.App.Version,
		info.Schema.Latest(), true
	return sc.SetOutput(ctx, out)
}

func stateMissing(msg string) error {
	return refuse(ClassTransferFailed, msg, "Start the move again with the same code; the old manager sends the same copy again.")
}

// verifyPackage runs the checks of stepVerify on dir.
func (s *Service) verifyPackage(ctx context.Context, dir string, in receiveInput, code string) (StateInfo, secrets.Key, error) {
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
			"Update this Docker Manager to at least "+info.App.Version+" first, then start the move again.")
	}
	if info.MoveID != in.MoveID {
		return StateInfo{}, secrets.Key{}, refuse(ClassCodeInvalid, "the received state belongs to another move", "Start the move again with the code shown on the old manager.")
	}
	sealed, err := os.ReadFile(filepath.Join(dir, PartSealedKey)) //nolint:gosec // below the data directory
	if err != nil {
		return StateInfo{}, secrets.Key{}, stateMissing("the received secret key is missing")
	}
	key, err := openSecretKey(sealed, code, info.InstanceID)
	if err != nil || key.ID() != info.SecretKeyID {
		return StateInfo{}, secrets.Key{}, refuse(ClassCodeInvalid, "the received secret key does not open with this move code",
			"Start the move again with the code shown on the old manager.")
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
			"Start the move again; if it fails again, the old manager's state is damaged.")
	}
	if s.opts.CheckSchema != nil {
		if err := s.opts.CheckSchema(ctx, db); err != nil {
			if errors.Is(err, store.ErrUnknownMigrations) {
				return refuse(ClassSchemaIncompatible, "the old manager ("+info.App.Version+") has a newer database than this Docker Manager knows",
					"Update this Docker Manager to at least "+info.App.Version+" first, then start the move again.")
			}
			return err
		}
	}
	if err := backups.CheckSealedSettings(ctx, db, key); err != nil {
		if errors.Is(err, backups.ErrSealedSettings) {
			return refuse(ClassStateInvalid, "the received secret key does not decrypt the received settings",
				"Start the move again; if it fails again, the old manager's secret key does not match its database.")
		}
		return err
	}
	m, err := store.GetManagerMove(ctx, db, info.MoveID)
	if err != nil || m.State != domain.MoveArrived {
		return refuse(ClassMoveNotHandedOff, "the received database does not record this move as handed off",
			"Start the move again with the code shown on the old manager.")
	}
	return nil
}

// stepStage stages the checked copy as a restore of kind move; the finish
// hook requests the controlled restart that applies it.
func (s *Service) stepStage(ctx context.Context, sc *jobexec.StepContext) error {
	in, code, err := s.receiveJob(sc)
	if err != nil {
		return err
	}
	var out receiveOutput
	if err := json.Unmarshal(sc.Output(), &out); err != nil || !out.Verified {
		return errors.New("the verification result is missing; start the move again")
	}
	dir := s.incomingDir(sc.JobID)
	key, err := secrets.LoadKeyFile(filepath.Join(dir, receiveKeyFile))
	if err != nil {
		return errors.New("the received state is missing; start the move again")
	}
	sealedCode, err := secrets.NewKeyring(key).Seal([]byte(code), SealContext(in.MoveID))
	if err != nil {
		return err
	}
	sc.Progress(ctx, 90, "staging the received state")
	drafts := filepath.Join(dir, PartTemplates)
	if _, err := os.Stat(drafts); err != nil {
		drafts = ""
	}
	mk := backups.RestoreMarker{Format: backups.RestoreMarkerFormat, Version: 1, Kind: backups.RestoreKindMove, JobID: sc.JobID,
		InstanceID: out.InstanceID, App: backup.AppInfo{Version: out.AppVersion}, SchemaLatest: out.SchemaLatest, Manifests: []backup.Manifest{},
		StagedAt: s.now(), MoveID: in.MoveID, SourceURL: in.SourceURL, SealedMoveCode: sealedCode, Generation: out.Generation}
	if err := backups.StageRestore(s.opts.DataDir, dir, filepath.Join(dir, PartDatabase), drafts, key, mk); err != nil {
		return err
	}
	out.RestartPending = true
	return sc.SetOutput(ctx, out)
}

// SealContext binds the sealed move code to its move.
func SealContext(moveID string) string { return "manager_moves/" + moveID + "/code" }

// onReceiveFinished forgets the code and, after success, asks for the
// controlled restart that applies the copy.
func (s *Service) onReceiveFinished(_ context.Context, _ bun.IDB, j domain.Job) error {
	s.recvMu.Lock()
	delete(s.codes, j.ID)
	s.recvMu.Unlock()
	if j.State != domain.JobSucceeded {
		_ = os.RemoveAll(s.incomingDir(j.ID))
		return nil
	}
	s.log.Warn("the moved manager state is staged; restarting to run it", "job_id", j.ID)
	if s.opts.RequestRestart != nil {
		s.opts.RequestRestart()
	}
	return nil
}
