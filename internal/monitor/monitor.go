// Package monitor backs `slfhst status` and `slfhst check-all`:
// service-status summary, port checks, DNSBL checks, anomaly-only email
// alerting. Direct port of monitor.py, with `bootc upgrade --check`
// replaced by internal/sysupdate.CheckNewVersion() (no bootc in this
// design at all).
package monitor

import (
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"

	"github.com/nnnithinnn/slfhst/internal/config"
	"github.com/nnnithinnn/slfhst/internal/dnscf"
	"github.com/nnnithinnn/slfhst/internal/firewall"
	"github.com/nnnithinnn/slfhst/internal/runx"
	"github.com/nnnithinnn/slfhst/internal/sysupdate"
)

var appServices = []string{
	"traefik", "postgres", "garage", "stalwart", "bulwark", "museum",
	"ente-web-photos", "ente-web-auth", "ente-web-locker", "vaultwarden",
}

var portChecks = []int{8443, 2525, 4465, 4993, 4190}

var dnsblZones = []string{"zen.spamhaus.org", "bl.spamcop.net", "b.barracudacentral.org"}

const (
	smtpHost = "127.0.0.1"
	smtpPort = 2525
)

// ServiceStatus reports each app service's `systemctl --user is-active`
// state, "unknown" if the check itself failed.
func ServiceStatus() map[string]string {
	out := make(map[string]string, len(appServices))
	for _, name := range appServices {
		res, err := runx.Run([]string{"systemctl", "--user", "is-active", name + ".service"},
			runx.Options{AsUser: config.ServiceUser, Capture: true, NoCheck: true})
		status := "unknown"
		if err == nil {
			if s := strings.TrimSpace(res.Stdout); s != "" {
				status = s
			}
		}
		out[name] = status
	}
	return out
}

// Status prints a service status summary, backing `slfhst status`.
func Status(store config.Store) error {
	statuses := ServiceStatus()
	for _, name := range appServices {
		fmt.Printf("%-20s %s\n", name, statuses[name])
	}
	return nil
}

func failedPorts() []int {
	var failed []int
	for _, port := range portChecks {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)), 3*time.Second)
		if err != nil {
			failed = append(failed, port)
			continue
		}
		conn.Close()
	}
	return failed
}

// lookupHost is a package-level var (not a direct net.LookupHost call)
// so tests can substitute a fake resolver instead of hitting real DNS.
var lookupHost = net.LookupHost

func reverseIPv4(ip string) (string, bool) {
	parts := strings.Split(ip, ".")
	if len(parts) != 4 {
		return "", false
	}
	return parts[3] + "." + parts[2] + "." + parts[1] + "." + parts[0], true
}

// dnsblListings returns the DNSBL zones that list ip -- a successful
// lookup of the reversed-IP query name means listed, matching
// monitor.py's dnsbl_check() (a not-listed lookup fails with NXDOMAIN).
func dnsblListings(ip string) []string {
	rev, ok := reverseIPv4(ip)
	if !ok {
		return nil
	}
	var listed []string
	for _, zone := range dnsblZones {
		if _, err := lookupHost(rev + "." + zone); err == nil {
			listed = append(listed, zone)
		}
	}
	return listed
}

func sendAlert(cfg map[string]any, subject, body string) {
	domain, _ := cfg["domain"].(string)
	alertEmail, _ := cfg["alert_email"].(string)
	if alertEmail == "" {
		fmt.Println("warning: monitor: no alert_email configured, not sending alert")
		return
	}
	from := "slfhst@" + domain
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: [slfhst] %s\r\n\r\n%s\r\n", from, alertEmail, subject, body)

	addr := net.JoinHostPort(smtpHost, fmt.Sprint(smtpPort))
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		fmt.Println("warning: monitor: alert relay unreachable:", err)
		return
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, smtpHost)
	if err != nil {
		fmt.Println("warning: monitor: alert SMTP handshake failed:", err)
		return
	}
	defer client.Close()

	if err := client.Mail(from); err != nil {
		fmt.Println("warning: monitor: alert MAIL FROM failed:", err)
		return
	}
	if err := client.Rcpt(alertEmail); err != nil {
		fmt.Println("warning: monitor: alert RCPT TO failed:", err)
		return
	}
	wc, err := client.Data()
	if err != nil {
		fmt.Println("warning: monitor: alert DATA failed:", err)
		return
	}
	if _, err := wc.Write([]byte(msg)); err != nil {
		fmt.Println("warning: monitor: alert write failed:", err)
		return
	}
	if err := wc.Close(); err != nil {
		fmt.Println("warning: monitor: alert send failed:", err)
		return
	}
	_ = client.Quit()
}

// CheckAll runs the periodic health/DNSBL/Cloudflare-ipset-refresh
// check, emailing an alert only if something's actually wrong. A soft
// no-op (not an error) if stage2 isn't done yet, matching monitor.py.
func CheckAll(store config.Store) error {
	if !store.IsDone("stage2") {
		fmt.Println("monitor: stage2 not done yet, skipping check-all")
		return nil
	}
	cfg, err := store.Load()
	if err != nil {
		return err
	}

	var issues []string

	statuses := ServiceStatus()
	for _, name := range appServices {
		if statuses[name] != "active" {
			issues = append(issues, fmt.Sprintf("service %s is %s", name, statuses[name]))
		}
	}

	for _, port := range failedPorts() {
		issues = append(issues, fmt.Sprintf("port %d is not accepting connections", port))
	}

	if ip, err := dnscf.PublicIP(); err != nil {
		issues = append(issues, fmt.Sprintf("could not determine public IP for DNSBL check: %v", err))
	} else if listed := dnsblListings(ip); len(listed) > 0 {
		issues = append(issues, fmt.Sprintf("listed on DNSBL(s): %s", strings.Join(listed, ", ")))
	}

	if err := firewall.SyncCloudflareIPSets(); err != nil {
		// Best-effort, matches monitor.py's broad except here.
		fmt.Println("warning: monitor: cf-ips sync failed during check-all:", err)
	}

	updateStatus := "up to date"
	if v, err := sysupdate.CheckNewVersion(); err != nil {
		updateStatus = fmt.Sprintf("update check failed: %v", err)
	} else if v != "" {
		updateStatus = "new version available: " + v
	}

	if len(issues) == 0 {
		fmt.Println("monitor: all clear")
		return nil
	}

	body := strings.Join(issues, "\n") + "\n\nOS update status: " + updateStatus
	sendAlert(cfg, "action needed", body)
	return nil
}
