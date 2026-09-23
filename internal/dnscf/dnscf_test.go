package dnscf

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nnnithinnn/slfhst/internal/config"
)

func TestPublicIP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "fl=1f1\nh=www.cloudflare.com\nip=203.0.113.7\nts=1234\n")
	}))
	defer srv.Close()

	old := traceURL
	traceURL = srv.URL
	defer func() { traceURL = old }()

	ip, err := PublicIP()
	if err != nil {
		t.Fatalf("PublicIP: %v", err)
	}
	if ip != "203.0.113.7" {
		t.Errorf("got %q, want 203.0.113.7", ip)
	}
}

// mockCF is a minimal in-memory Cloudflare API double: one zone, a
// mutable record store, supporting the GET/POST/PUT calls Sync() makes.
type mockCF struct {
	mu      sync.Mutex
	records []dnsRecord
	nextID  int
	calls   []string // "METHOD path" for assertions
}

func newMockCF() *mockCF {
	return &mockCF{records: []dnsRecord{{ID: "existing-a", Type: "A", Name: "smtp.example.com", Content: "203.0.113.7", TTL: 1}}}
}

func (m *mockCF) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.calls = append(m.calls, r.Method+" "+r.URL.Path)

		switch {
		case r.Method == "GET" && r.URL.Path == "/zones":
			writeEnvelope(w, []map[string]string{{"id": "zone123"}})
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/dns_records"):
			writeEnvelope(w, m.records)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/dns_records"):
			var rec dnsRecord
			json.NewDecoder(r.Body).Decode(&rec)
			m.nextID++
			rec.ID = fmt.Sprintf("new-%d", m.nextID)
			m.records = append(m.records, rec)
			writeEnvelope(w, rec)
		case r.Method == "PUT" && strings.Contains(r.URL.Path, "/dns_records/"):
			id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			var rec dnsRecord
			json.NewDecoder(r.Body).Decode(&rec)
			for i := range m.records {
				if m.records[i].ID == id {
					rec.ID = id
					m.records[i] = rec
				}
			}
			writeEnvelope(w, rec)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}
}

func writeEnvelope(w http.ResponseWriter, result any) {
	json.NewEncoder(w).Encode(map[string]any{"success": true, "errors": []any{}, "result": result})
}

func TestSyncCreatesAndUpdatesRecords(t *testing.T) {
	mock := newMockCF()
	apiSrv := httptest.NewServer(mock.handler())
	defer apiSrv.Close()
	traceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ip=203.0.113.7\n")
	}))
	defer traceSrv.Close()

	oldAPI, oldTrace := apiBase, traceURL
	apiBase, traceURL = apiSrv.URL, traceSrv.URL
	defer func() { apiBase, traceURL = oldAPI, oldTrace }()

	store := config.Store{Root: t.TempDir()}
	if err := store.MarkDone("stage1"); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(map[string]any{
		"domain":               "example.com",
		"cloudflare_api_token": "tok",
		"alert_email":          "admin@example.com",
	}); err != nil {
		t.Fatal(err)
	}

	if err := Sync(store); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	byName := map[string]dnsRecord{}
	for _, r := range mock.records {
		byName[r.Type+" "+r.Name] = r
	}

	// smtp.example.com A record already existed with the right content
	// -- Sync should have left it alone (a PUT would still be harmless,
	// but no-op is the whole point of the upsert diff).
	if got := countCalls(mock.calls, "PUT"); got != 0 {
		t.Errorf("expected 0 PUT calls (smtp record already matched), got %d: %v", got, mock.calls)
	}

	want := []string{
		"A mail.example.com", "A vault.example.com", "A photos.example.com",
		"A auth.example.com", "A locker.example.com", "A api.example.com",
		"A smtp.example.com", "MX example.com", "TXT example.com", "TXT _dmarc.example.com",
	}
	for _, w := range want {
		if _, ok := byName[w]; !ok {
			t.Errorf("missing expected record %q after Sync; have %v", w, keys(byName))
		}
	}

	mx := byName["MX example.com"]
	if mx.Content != "smtp.example.com" || mx.Priority == nil || *mx.Priority != 10 {
		t.Errorf("MX record = %+v, want content smtp.example.com priority 10", mx)
	}

	dmarc := byName["TXT _dmarc.example.com"]
	if dmarc.Content != "v=DMARC1; p=quarantine; rua=mailto:admin@example.com" {
		t.Errorf("DMARC record content = %q", dmarc.Content)
	}

	for _, sub := range subdomains {
		r := byName["A "+sub+".example.com"]
		if !r.Proxied {
			t.Errorf("%s A record should be proxied", sub)
		}
	}
	if byName["A smtp.example.com"].Proxied {
		t.Error("smtp A record should NOT be proxied")
	}
}

func TestSyncRequiresStage1(t *testing.T) {
	store := config.Store{Root: t.TempDir()}
	err := Sync(store)
	if err == nil {
		t.Fatal("expected an error when stage1 isn't done")
	}
}

func countCalls(calls []string, method string) int {
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, method+" ") {
			n++
		}
	}
	return n
}

func keys(m map[string]dnsRecord) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
