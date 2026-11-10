package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"github.com/zatca-go/zatca/config"
	"github.com/zatca-go/zatca/queue"
)

type natsRuntime struct {
	conn   *nats.Conn
	server *server.Server
}

func openNATS(cfg config.NATSConfig, monitorPassword string, readiness func(context.Context) error) (*natsRuntime, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	mode, _ := config.ParseNATSMode(string(cfg.Mode))
	if mode == config.NATSExternal {
		conn, err := nats.Connect(cfg.URL, natsAuthOptions(cfg)...)
		if err != nil {
			return nil, fmt.Errorf("connect to external NATS: %w", err)
		}
		return &natsRuntime{conn: conn}, nil
	}

	opts := &server.Options{
		Port:      cfg.Port,
		JetStream: true,
		StoreDir:  cfg.DataDir,
		NoLog:     true,
		NoSigs:    true,
	}
	if cfg.Username != "" {
		opts.Users = []*server.User{{
			Username: cfg.Username,
			Password: cfg.Password,
		}}
	}
	if cfg.MonitorPort > 0 {
		if monitorPassword == "" {
			return nil, fmt.Errorf("monitor password is required when NATS monitoring is enabled")
		}
		internalPort := cfg.MonitorPort + 10000
		opts.HTTPHost = "127.0.0.1"
		opts.HTTPPort = internalPort
		target, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", internalPort))
		proxy := httputil.NewSingleHostReverseProxy(target)
		mux := http.NewServeMux()
		mux.Handle("/", basicAuth(daemonMonitorHandler(readiness, proxy), monitorPassword))
		go func() {
			addr := fmt.Sprintf(":%d", cfg.MonitorPort)
			if err := http.ListenAndServe(addr, mux); err != nil {
				fmt.Printf("WARN: NATS monitoring server: %v\n", err)
			}
		}()
	}
	ns, err := server.NewServer(opts)
	if err != nil {
		return nil, fmt.Errorf("create embedded NATS: %w", err)
	}
	go ns.Start()
	if !ns.ReadyForConnections(10 * time.Second) {
		ns.Shutdown()
		return nil, fmt.Errorf("embedded NATS was not ready")
	}
	conn, err := nats.Connect(ns.ClientURL(), natsAuthOptions(cfg)...)
	if err != nil {
		ns.Shutdown()
		return nil, fmt.Errorf("connect to embedded NATS: %w", err)
	}
	return &natsRuntime{conn: conn, server: ns}, nil
}

func daemonMonitorHandler(readiness func(context.Context) error, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if readiness != nil && r.URL.Path == "/healthz" {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			if err := readiness(ctx); err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"status": "unhealthy",
					"error":  err.Error(),
				})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func natsAuthOptions(cfg config.NATSConfig) []nats.Option {
	if cfg.Username == "" {
		return nil
	}
	return []nats.Option{nats.UserInfo(cfg.Username, cfg.Password)}
}

func (r *natsRuntime) close() {
	if r == nil {
		return
	}
	if r.conn != nil {
		_ = r.conn.Drain()
		r.conn.Close()
	}
	if r.server != nil {
		r.server.Shutdown()
		r.server.WaitForShutdown()
	}
}

func ensureStream(js nats.JetStreamContext, mode config.NATSMode) error {
	info, err := js.StreamInfo(queue.StreamName)
	if err != nil {
		if mode == config.NATSEmbedded {
			_, err = js.AddStream(&nats.StreamConfig{
				Name:      queue.StreamName,
				Subjects:  []string{queue.SubjectAll},
				Retention: nats.WorkQueuePolicy,
				Storage:   nats.FileStorage,
				MaxAge:    7 * 24 * time.Hour,
			})
			if err != nil {
				return fmt.Errorf("create embedded ZATCA stream: %w", err)
			}
			return nil
		}
		return fmt.Errorf("shared NATS stream %q is unavailable: %w", queue.StreamName, err)
	}
	if info == nil {
		return fmt.Errorf("NATS returned no stream info for %q", queue.StreamName)
	}
	if info.Config.Retention != nats.WorkQueuePolicy {
		return fmt.Errorf("NATS stream %q must use work-queue retention", queue.StreamName)
	}
	for _, subject := range info.Config.Subjects {
		if subject == queue.SubjectAll {
			return nil
		}
	}
	return fmt.Errorf("NATS stream %q does not cover %q", queue.StreamName, queue.SubjectAll)
}
