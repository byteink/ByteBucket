package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// issuing a real HTTP request through the metrics middleware, then scraping
// /metrics, must produce a counter line for that route. Anything less means
// the middleware silently dropped the observation and dashboards would lie.
func TestMetricsCounterIncrementsOnRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Metrics())
	r.GET("/probe", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	r.GET("/metrics", gin.WrapH(PrometheusHandler()))

	// Fire the instrumented route.
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("probe request failed: %d", w.Code)
	}

	// Scrape.
	scrapeReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	scrapeW := httptest.NewRecorder()
	r.ServeHTTP(scrapeW, scrapeReq)
	if scrapeW.Code != http.StatusOK {
		t.Fatalf("scrape failed: %d", scrapeW.Code)
	}

	body := scrapeW.Body.String()
	// Expected counter line. Exact label match keeps the assertion tight;
	// a looser substring check would pass even if the route label leaked
	// a raw path.
	needle := `http_requests_total{method="GET",path="/probe",status="200"} 1`
	if !strings.Contains(body, needle) {
		t.Fatalf("expected scrape body to contain %q; got:\n%s", needle, body)
	}
	// The metrics endpoint itself must not be instrumented — otherwise
	// every scrape inflates its own counter and rate() becomes junk.
	if strings.Contains(body, `path="/metrics"`) {
		t.Fatalf("/metrics endpoint must not be counted in http_requests_total; got:\n%s", body)
	}
}
