// Package dnscf syncs Cloudflare DNS records for the appliance's domain
// (mail/vault/photos/auth/locker/api subdomains, MX, SPF, DMARC, DKIM).
// Direct port of dns_cf.py -- net/http + encoding/json replacing
// urllib.request.
package dnscf

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nnnithinnn/slfhst/internal/config"
)

// apiBase and traceURL are vars, not consts, so tests can point them at
// an httptest server instead of the real Cloudflare API.
var (
	apiBase  = "https://api.cloudflare.com/client/v4"
	traceURL = "https://www.cloudflare.com/cdn-cgi/trace"
)

// subdomains get a proxied A record pointing at this box.
var subdomains = []string{"mail", "vault", "photos", "auth", "locker", "api"}

type dnsRecord struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type"`
	Name     string `json:"name"`
	Content  string `json:"content"`
	TTL      int    `json:"ttl"`
	Proxied  bool   `json:"proxied"`
	Priority *int   `json:"priority,omitempty"`
}

type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type apiEnvelope struct {
	Success bool       `json:"success"`
	Errors  []apiError `json:"errors"`
}

// PublicIP returns this box's public IPv4/IPv6 address per Cloudflare's
// own trace endpoint. Direct port of dns_cf.py's public_ip().
func PublicIP() (string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(traceURL)
	if err != nil {
		return "", fmt.Errorf("dnscf: fetch %s: %w", traceURL, err)
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if ip, ok := strings.CutPrefix(line, "ip="); ok {
			return ip, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("dnscf: read trace response: %w", err)
	}
	return "", fmt.Errorf("dnscf: no ip= line in trace response")
}

func request(method, path, token string, body, out any) error {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("dnscf: marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, apiBase+path, bodyReader)
	if err != nil {
		return fmt.Errorf("dnscf: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("dnscf: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("dnscf: %s %s: read response: %w", method, path, err)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("dnscf: %s %s: HTTP %d: %s", method, path, resp.StatusCode, string(data))
	}

	var env apiEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("dnscf: %s %s: parse envelope: %w", method, path, err)
	}
	if !env.Success {
		return fmt.Errorf("dnscf: %s %s: API reported failure: %+v", method, path, env.Errors)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("dnscf: %s %s: parse result: %w", method, path, err)
		}
	}
	return nil
}

func zoneID(domain, token string) (string, error) {
	var result struct {
		Result []struct {
			ID string `json:"id"`
		} `json:"result"`
	}
	if err := request("GET", "/zones?name="+url.QueryEscape(domain), token, nil, &result); err != nil {
		return "", err
	}
	if len(result.Result) == 0 {
		return "", fmt.Errorf("dnscf: no Cloudflare zone found for domain %q", domain)
	}
	return result.Result[0].ID, nil
}

func existingRecords(zoneID, token string) ([]dnsRecord, error) {
	var result struct {
		Result []dnsRecord `json:"result"`
	}
	path := fmt.Sprintf("/zones/%s/dns_records?per_page=200", zoneID)
	if err := request("GET", path, token, nil, &result); err != nil {
		return nil, err
	}
	return result.Result, nil
}

// upsert creates rec if no existing record matches its (type, name,
// priority), updates it if the content/proxied state differs, or is a
// no-op if it already matches. Direct port of dns_cf.py's _upsert().
func upsert(zoneID, token string, existing []dnsRecord, rec dnsRecord) error {
	for _, e := range existing {
		if e.Type != rec.Type || e.Name != rec.Name || !samePriority(e.Priority, rec.Priority) {
			continue
		}
		if e.Content == rec.Content && e.Proxied == rec.Proxied {
			return nil
		}
		path := fmt.Sprintf("/zones/%s/dns_records/%s", zoneID, e.ID)
		return request("PUT", path, token, rec, nil)
	}
	path := fmt.Sprintf("/zones/%s/dns_records", zoneID)
	return request("POST", path, token, rec, nil)
}

func samePriority(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func intPtr(v int) *int { return &v }

// stalwartDKIMTXT would return Stalwart's DKIM public key as a TXT
// record value -- stub, direct port of dns_cf.py's _stalwart_dkim_txt(),
// which is itself a stub (no programmatic way yet to pull it out of
// Stalwart). Always nil for now; add default._domainkey manually until
// this is wired up.
func stalwartDKIMTXT(_ map[string]any) (string, bool) {
	return "", false
}

// Sync reads cfg["domain"]/["cloudflare_api_token"] and upserts this
// appliance's DNS records against Cloudflare's API. Direct port of
// dns_cf.py's sync().
func Sync(store config.Store) error {
	if err := store.RequireDone("stage1"); err != nil {
		return err
	}
	cfg, err := store.Load()
	if err != nil {
		return err
	}

	domain, _ := cfg["domain"].(string)
	token, _ := cfg["cloudflare_api_token"].(string)
	alertEmail, _ := cfg["alert_email"].(string)
	if domain == "" || token == "" {
		return fmt.Errorf("dnscf: config.json is missing domain/cloudflare_api_token")
	}

	ip, err := PublicIP()
	if err != nil {
		return err
	}
	zone, err := zoneID(domain, token)
	if err != nil {
		return err
	}
	existing, err := existingRecords(zone, token)
	if err != nil {
		return err
	}

	for _, sub := range subdomains {
		rec := dnsRecord{Type: "A", Name: sub + "." + domain, Content: ip, TTL: 1, Proxied: true}
		if err := upsert(zone, token, existing, rec); err != nil {
			return err
		}
	}

	smtpRec := dnsRecord{Type: "A", Name: "smtp." + domain, Content: ip, TTL: 1, Proxied: false}
	if err := upsert(zone, token, existing, smtpRec); err != nil {
		return err
	}

	mxRec := dnsRecord{Type: "MX", Name: domain, Content: "smtp." + domain, TTL: 1, Proxied: false, Priority: intPtr(10)}
	if err := upsert(zone, token, existing, mxRec); err != nil {
		return err
	}

	spfRec := dnsRecord{Type: "TXT", Name: domain, Content: "v=spf1 mx ~all", TTL: 1, Proxied: false}
	if err := upsert(zone, token, existing, spfRec); err != nil {
		return err
	}

	if alertEmail != "" {
		dmarcRec := dnsRecord{
			Type: "TXT", Name: "_dmarc." + domain,
			Content: fmt.Sprintf("v=DMARC1; p=quarantine; rua=mailto:%s", alertEmail),
			TTL:     1, Proxied: false,
		}
		if err := upsert(zone, token, existing, dmarcRec); err != nil {
			return err
		}
	}

	if dkim, ok := stalwartDKIMTXT(cfg); ok {
		dkimRec := dnsRecord{Type: "TXT", Name: "default._domainkey." + domain, Content: dkim, TTL: 1, Proxied: false}
		if err := upsert(zone, token, existing, dkimRec); err != nil {
			return err
		}
	} else {
		fmt.Println("warning: dnscf: no DKIM key available yet -- add default._domainkey TXT manually")
	}

	return nil
}
