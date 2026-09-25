package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// The control listener (-control, loopback only) lets tests change a
// host's Docker Engine directly, bypassing DockYard, as `docker stop` on
// the host would (#23: changes made directly through Docker on either host
// appear in every open view without reload). The fake Engine emits the
// same Docker events a real one does; the agent's production event relay
// forwards them.
//
//	POST /engines/{environment}/containers/{container}/stop
//	POST /engines/{environment}/containers/{container}/start
//
// {environment} is the host name (homelab, nas); {container} a name or ID.
func (s *seeder) serveControl(ctx context.Context, addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("refusing to serve the control listener on %s: loopback only", addr)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /engines/{environment}/containers/{container}/{action}", func(w http.ResponseWriter, r *http.Request) {
		h := s.host(r.PathValue("environment"))
		if h == nil {
			http.Error(w, "no such environment", http.StatusNotFound)
			return
		}
		name := r.PathValue("container")
		c, ok := h.engine.Container(name)
		if !ok {
			http.Error(w, "no such container", http.StatusNotFound)
			return
		}
		var opErr error
		switch r.PathValue("action") {
		case "stop":
			opErr = h.engine.StopContainer(r.Context(), c.Details.ID, nil)
		case "start":
			opErr = h.engine.StartContainer(r.Context(), c.Details.ID)
		default:
			http.Error(w, "unknown action", http.StatusNotFound)
			return
		}
		if opErr != nil {
			http.Error(w, opErr.Error(), http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Warn("control listener stopped", slog.Any("error", err))
		}
	}()
	return nil
}

func (s *seeder) host(name string) *homelabHost {
	for _, h := range s.hosts {
		if h.name == name {
			return h
		}
	}
	return nil
}
