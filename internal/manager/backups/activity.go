package backups

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// Live activity of running backups (#10): the latest activity report of
// each backup job, in memory only. Reports carry the file restic reads,
// so they never reach the job record, its events, the bus or the audit
// trail; the API shows the file only to holders of the scope's
// files-read capability.

// activityStale is when a report no longer describes a running job.
const activityStale = 30 * time.Second

// maxActivityJobs bounds the reports kept (running backups at once).
const maxActivityJobs = 1024

type activityEntry struct {
	env    string
	report protocol.ActivityPayload
	at     time.Time
}

type activityStore struct {
	mu   sync.Mutex
	jobs map[string]activityEntry
}

// recordActivity keeps a report (engine OnActivity listener).
func (s *Service) recordActivity(env, jobID string, a protocol.ActivityPayload) {
	s.activity.mu.Lock()
	defer s.activity.mu.Unlock()
	if s.activity.jobs == nil {
		s.activity.jobs = map[string]activityEntry{}
	}
	if _, ok := s.activity.jobs[jobID]; !ok && len(s.activity.jobs) >= maxActivityJobs {
		return
	}
	s.activity.jobs[jobID] = activityEntry{env: env, report: a, at: s.now()}
}

// forgetActivity drops a finished job's report.
func (s *Service) forgetActivity(jobID string) {
	s.activity.mu.Lock()
	defer s.activity.mu.Unlock()
	delete(s.activity.jobs, jobID)
}

// BackupActivity is a running (or waiting) backup job with its latest
// activity report.
type BackupActivity struct {
	Job      domain.Job
	SetID    string
	PolicyID string
	// Items are a backup.run job's items (none for the manager state,
	// whose one item is backup.ItemManagerState).
	Items []protocol.BackupItem
	// Report is the latest report (nil before the first one or when it
	// is stale); ReportedAt when it arrived.
	Report     *protocol.ActivityPayload
	ReportedAt time.Time
}

// Activity lists the backup jobs that have not finished, newest first,
// with their latest reports. Callers authorize each job (job.read) and
// the current file (the scope's files-read capability).
func (s *Service) Activity(ctx context.Context) ([]BackupActivity, error) {
	js, err := s.opts.Jobs.List(ctx, domain.JobFilter{Kinds: []domain.JobKind{jobspec.BackupRun, jobspec.ManagerBackup},
		States: []domain.JobState{domain.JobQueued, domain.JobBlocked, domain.JobDispatched, domain.JobRunning, domain.JobCancelling},
		Limit:  200})
	if err != nil {
		return nil, err
	}
	now := s.now()
	s.activity.mu.Lock()
	defer s.activity.mu.Unlock()
	out := make([]BackupActivity, 0, len(js))
	for _, j := range js {
		a := BackupActivity{Job: j}
		var in struct {
			SetID    string                `json:"setId"`
			PolicyID string                `json:"policyId"`
			Items    []protocol.BackupItem `json:"items"`
		}
		_ = json.Unmarshal(j.Input, &in)
		a.SetID, a.PolicyID = in.SetID, in.PolicyID
		if j.Kind == jobspec.BackupRun {
			a.Items = in.Items
		}
		// Only reports from the job's own environment (an agent names
		// the job ID it reports for) that are still fresh.
		if e, ok := s.activity.jobs[j.ID]; ok && e.env == j.EnvironmentID && now.Sub(e.at) < activityStale && j.State.Active() {
			r := e.report
			a.Report, a.ReportedAt = &r, e.at
		}
		out = append(out, a)
	}
	return out, nil
}

// ListLocations returns the locations of a repository ("" = all), with
// their measured sizes.
func (s *Service) ListLocations(ctx context.Context, repositoryID string) ([]domain.BackupLocation, error) {
	return store.ListBackupLocations(ctx, s.db, repositoryID)
}
