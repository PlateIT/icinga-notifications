package listener

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// Metrics use a separate, opt-in listener. Never register them on the public event API.
func (l *Listener) startMetricsServer(ctx context.Context) error {
	address := os.Getenv("ICINGA_NOTIFICATIONS_METRICS_ADDRESS")
	if address == "" {
		return nil
	}
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("metrics listener: %w", err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metricsHandler(l.collectMetrics))
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 3 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	go func() { _ = server.Serve(ln) }()
	go func() { <-ctx.Done(); _ = server.Close() }()
	return nil
}

type operatingMetrics struct {
	Pending, Processing, Errors, Oldest int64
	Sent, Failed                        int64
	DeliveriesPending                   int64
}

func (l *Listener) collectMetrics(ctx context.Context) (operatingMetrics, error) {
	var m operatingMetrics
	queries := []struct {
		dest  any
		query string
		args  []any
	}{
		{&m.Pending, "SELECT COUNT(*) FROM job_queue WHERE state = 0", nil},
		{&m.Processing, "SELECT COUNT(*) FROM job_queue WHERE state = 1", nil},
		{&m.Errors, "SELECT COUNT(*) FROM job_queue WHERE state = 64", nil},
		{&m.Oldest, "SELECT COALESCE(MIN(last_update), 0) FROM job_queue WHERE state = 0", nil},
		{&m.DeliveriesPending, "SELECT COUNT(*) FROM incident_history WHERE notification_state = 'pending'", nil},
		{&m.Sent, "SELECT COUNT(*) FROM notification_history WHERE state = 'sent' AND triggered_at >= ?", []any{time.Now().Add(-5 * time.Minute).UnixMilli()}},
		{&m.Failed, "SELECT COUNT(*) FROM notification_history WHERE state = 'failed' AND triggered_at >= ?", []any{time.Now().Add(-5 * time.Minute).UnixMilli()}},
	}
	for _, q := range queries {
		if err := l.db.GetContext(ctx, q.dest, l.db.Rebind(q.query), q.args...); err != nil {
			return m, err
		}
	}
	return m, nil
}

func metricsHandler(collect func(context.Context) (operatingMetrics, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		m, err := collect(ctx)
		if err != nil {
			http.Error(w, "Metrics unavailable", http.StatusServiceUnavailable)
			return
		}
		var out strings.Builder
		metric := func(name, help string, value float64) {
			fmt.Fprintf(&out, "# HELP icinga_notifications_%s %s\n# TYPE icinga_notifications_%s gauge\nicinga_notifications_%s %g\n", name, help, name, name, value)
		}
		metric("events_pending", "Events waiting for processing (shared database).", float64(m.Pending))
		metric("events_processing", "Events currently being processed (shared database).", float64(m.Processing))
		metric("events_failed", "Events in error state (shared database).", float64(m.Errors))
		age := 0.0
		if m.Oldest > 0 {
			age = max(0, float64(time.Now().UnixMilli()-m.Oldest)/1000)
		}
		metric("oldest_event_age_seconds", "Age of oldest pending event; zero for empty queue.", age)
		metric("deliveries_pending", "Notifications awaiting delivery (shared database).", float64(m.DeliveriesPending))
		metric("deliveries_sent_per_second", "Successful deliveries per second over the last five minutes.", float64(m.Sent)/300)
		metric("deliveries_failed_per_second", "Failed deliveries per second over the last five minutes.", float64(m.Failed)/300)
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(out.String()))
	})
}
