package listener

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOperatingMetrics(t *testing.T) {
	w := httptest.NewRecorder()
	metricsHandler(func(ctx context.Context) (operatingMetrics, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("unbounded scrape")
		}
		return operatingMetrics{Sent: 600, Failed: 30}, nil
	}).ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	for _, want := range []string{"icinga_notifications_deliveries_sent_per_second 2\n", "icinga_notifications_deliveries_failed_per_second 0.1\n", "icinga_notifications_oldest_event_age_seconds 0\n"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("missing %q", want)
		}
	}
}

func TestMetricsFailureDoesNotReportZeroOrLeakDetails(t *testing.T) {
	w := httptest.NewRecorder()
	metricsHandler(func(context.Context) (operatingMetrics, error) {
		return operatingMetrics{}, errors.New("private database details")
	}).ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	if w.Code != 503 || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "events_pending") {
		t.Fatal(w.Code, w.Body.String())
	}
}
