package diagnostics

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// unfinishedStates are the job states counted as the queue.
var unfinishedStates = []domain.JobState{domain.JobQueued, domain.JobBlocked, domain.JobDispatched, domain.JobRunning, domain.JobCancelling}

// promWriter writes the Prometheus text format (families with HELP/TYPE,
// escaped label values). It is pure Go: no client library (#34).
type promWriter struct {
	w   *bufio.Writer
	err error
}

type label struct{ k, v string }

func (p *promWriter) family(name, typ, help string) {
	p.printf("# HELP %s %s\n# TYPE %s %s\n", name, escapeHelp(help), name, typ)
}

func (p *promWriter) sample(name string, value float64, labels ...label) {
	var b strings.Builder
	b.WriteString(name)
	if len(labels) > 0 {
		b.WriteByte('{')
		for i, l := range labels {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(l.k)
			b.WriteString(`="`)
			b.WriteString(escapeLabel(l.v))
			b.WriteByte('"')
		}
		b.WriteByte('}')
	}
	p.printf("%s %s\n", b.String(), strconv.FormatFloat(value, 'g', -1, 64))
}

func (p *promWriter) printf(format string, args ...any) {
	if p.err == nil {
		_, p.err = fmt.Fprintf(p.w, format, args...)
	}
}

func escapeLabel(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`).Replace(s)
}

func escapeHelp(s string) string { return strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(s) }

// WriteMetrics writes the manager's internal metrics in the Prometheus
// text format: build info, job queue, agent sessions and environments,
// agent version compatibility, event streams and bus subscribers, database
// sizes, the audit chain length and Go runtime basics. No label carries a
// user-chosen value except environment-independent enums (states, kinds).
func (s *Service) WriteMetrics(ctx context.Context, w io.Writer) error {
	p := &promWriter{w: bufio.NewWriter(w)}
	b := s.o.Build
	p.family("docker_manager_build_info", "gauge", "Build of this Docker Manager (always 1).")
	p.sample("docker_manager_build_info", 1, label{"version", b.Version}, label{"commit", b.Commit}, label{"go_version", b.GoVersion})

	counts, err := store.CountJobs(ctx, s.o.DB)
	if err != nil {
		return err
	}
	byState := map[domain.JobState]int{}
	byKind := map[string]int{}
	for _, c := range counts {
		byState[c.State] += c.Count
		if slices.Contains(unfinishedStates, c.State) {
			byKind[string(c.Kind)] += c.Count
		}
	}
	p.family("docker_manager_jobs", "gauge", "Unfinished jobs by state (the job queue).")
	for _, st := range unfinishedStates {
		p.sample("docker_manager_jobs", float64(byState[st]), label{"state", string(st)})
	}
	p.family("docker_manager_job_queue_depth", "gauge", "Jobs waiting to run (queued or blocked).")
	p.sample("docker_manager_job_queue_depth", float64(byState[domain.JobQueued]+byState[domain.JobBlocked]))
	p.family("docker_manager_jobs_unfinished_by_kind", "gauge", "Unfinished jobs by job kind.")
	kinds := make([]string, 0, len(byKind))
	for k := range byKind {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		p.sample("docker_manager_jobs_unfinished_by_kind", float64(byKind[k]), label{"kind", k})
	}

	sessions := 0
	if s.o.Sessions != nil {
		sessions = len(s.o.Sessions())
	}
	p.family("docker_manager_agent_sessions", "gauge", "Connected agent sessions.")
	p.sample("docker_manager_agent_sessions", float64(sessions))

	envs, err := s.o.Environments.ListEnvironments(ctx, domain.EnvironmentFilter{
		Statuses: []domain.EnvironmentStatus{domain.EnvironmentActive, domain.EnvironmentArchived}})
	if err != nil {
		return err
	}
	type envKey struct {
		status string
		online bool
	}
	envCount := map[envKey]int{}
	for _, e := range envs {
		envCount[envKey{string(e.Status), e.Online}]++
	}
	p.family("docker_manager_environments", "gauge", "Environments by status and connection state.")
	for _, st := range []string{string(domain.EnvironmentActive), string(domain.EnvironmentArchived)} {
		for _, on := range []bool{true, false} {
			p.sample("docker_manager_environments", float64(envCount[envKey{st, on}]), label{"status", st}, label{"online", strconv.FormatBool(on)})
		}
	}
	agents, err := s.o.Environments.ListAgents(ctx, domain.AgentFilter{Statuses: []domain.AgentStatus{domain.AgentActive}})
	if err != nil {
		return err
	}
	compat := map[string]int{}
	for _, a := range agents {
		st, _ := protocol.AgentCompatibility(b.Version, a.Version)
		compat[st]++
	}
	p.family("docker_manager_agents", "gauge", "Active agents by version compatibility with this manager.")
	for _, st := range []string{protocol.VersionCurrent, protocol.VersionOutdated, protocol.VersionUnsupported} {
		p.sample("docker_manager_agents", float64(compat[st]), label{"compatibility", st})
	}

	var streams int64
	if s.o.SSEStreams != nil {
		streams = s.o.SSEStreams()
	}
	p.family("docker_manager_sse_streams", "gauge", "Open server-sent event streams (live UI, job, log and event streams).")
	p.sample("docker_manager_sse_streams", float64(streams))
	subs := 0
	if s.o.BusSubscribers != nil {
		subs = s.o.BusSubscribers()
	}
	p.family("docker_manager_event_bus_subscribers", "gauge", "Subscribers of the manager's internal event bus (streams and services).")
	p.sample("docker_manager_event_bus_subscribers", float64(subs))

	p.family("docker_manager_database_size_bytes", "gauge", "Size of the manager's SQLite files including the WAL.")
	p.sample("docker_manager_database_size_bytes", float64(fileSize(s.o.DatabasePath)), label{"database", "main"})
	p.sample("docker_manager_database_size_bytes", float64(fileSize(s.o.MetricsPath)), label{"database", "metrics"})

	chain, err := store.GetAuditChain(ctx, s.o.DB)
	if err != nil {
		return err
	}
	p.family("docker_manager_audit_chain_records", "gauge", "Retained audit records (the verifiable chain length).")
	p.sample("docker_manager_audit_chain_records", float64(chain.RecordCount))
	p.family("docker_manager_audit_chain_head_seq", "gauge", "Sequence number of the newest audit record.")
	p.sample("docker_manager_audit_chain_head_seq", float64(chain.HeadSeq))

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	p.family("go_goroutines", "gauge", "Number of goroutines that currently exist.")
	p.sample("go_goroutines", float64(runtime.NumGoroutine()))
	p.family("go_memstats_heap_alloc_bytes", "gauge", "Number of heap bytes allocated and still in use.")
	p.sample("go_memstats_heap_alloc_bytes", float64(ms.HeapAlloc))
	if p.err != nil {
		return p.err
	}
	return p.w.Flush()
}
