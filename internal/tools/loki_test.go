package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func FuzzLokiExecute(f *testing.F) { //nolint:dupl // Similar fuzz test exists for PrometheusQuery.Execute, but the input parameters and expected output are different enough that it's worth having a separate test.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"success","data":{"resultType":"streams","result":[]}}`)
	}))
	defer srv.Close()

	loki := NewLokiQuery(srv.URL, "test")

	f.Add(`{"query":"{job=\"varlogs\"}"}`)
	f.Add(`{"query":""}`)
	f.Add(`{}`)
	f.Add(`not json`)
	f.Add(`{"query":"{node=\"host\"} |= \"error\"","start":"2026-01-01T00:00:00Z","end":"2026-01-01T01:00:00Z","limit":50}`)
	f.Add(`{"query":"{job=\"a\"}","limit":-1}`)
	f.Add(`{"query":"{job=\"a\"}","limit":99999}`)
	f.Add(string([]byte{0x00, 0xff, 0xfe}))

	f.Fuzz(func(_ *testing.T, params string) {
		// Must not panic
		_, _ = loki.Execute(context.Background(), json.RawMessage(params))
	})
}
