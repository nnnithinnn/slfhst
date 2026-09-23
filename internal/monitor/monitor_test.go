package monitor

import (
	"fmt"
	"net"
	"testing"

	"github.com/nnnithinnn/slfhst/internal/config"
)

func TestReverseIPv4(t *testing.T) {
	got, ok := reverseIPv4("203.0.113.7")
	if !ok || got != "7.113.0.203" {
		t.Errorf("reverseIPv4(203.0.113.7) = (%q, %v), want (7.113.0.203, true)", got, ok)
	}
	if _, ok := reverseIPv4("not-an-ip"); ok {
		t.Error("reverseIPv4 should reject a non-IPv4 string")
	}
	if _, ok := reverseIPv4("2001:db8::1"); ok {
		t.Error("reverseIPv4 should reject an IPv6 address (DNSBL zones used here are v4-only)")
	}
}

func TestDNSBLListings(t *testing.T) {
	old := lookupHost
	defer func() { lookupHost = old }()

	lookupHost = func(host string) ([]string, error) {
		if host == "7.113.0.203.zen.spamhaus.org" {
			return []string{"127.0.0.2"}, nil
		}
		return nil, fmt.Errorf("no such host")
	}

	listed := dnsblListings("203.0.113.7")
	if len(listed) != 1 || listed[0] != "zen.spamhaus.org" {
		t.Errorf("dnsblListings = %v, want [zen.spamhaus.org]", listed)
	}
}

func TestDNSBLListingsClean(t *testing.T) {
	old := lookupHost
	defer func() { lookupHost = old }()
	lookupHost = func(host string) ([]string, error) { return nil, fmt.Errorf("no such host") }

	if listed := dnsblListings("203.0.113.7"); len(listed) != 0 {
		t.Errorf("expected no listings, got %v", listed)
	}
}

func TestFailedPorts(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	openPort := ln.Addr().(*net.TCPAddr).Port

	old := portChecks
	portChecks = []int{openPort, 1} // port 1 should be closed/refused in any normal sandbox.
	defer func() { portChecks = old }()

	failed := failedPorts()
	for _, p := range failed {
		if p == openPort {
			t.Errorf("port %d is open and listening but was reported failed", openPort)
		}
	}
	found1 := false
	for _, p := range failed {
		if p == 1 {
			found1 = true
		}
	}
	if !found1 {
		t.Log("port 1 was not reported as failed -- unusual sandbox networking, not necessarily a bug")
	}
}

func TestCheckAllNoopBeforeStage2(t *testing.T) {
	store := config.Store{Root: t.TempDir()}
	// stage2 marker deliberately not created.
	if err := CheckAll(store); err != nil {
		t.Fatalf("CheckAll before stage2 should be a soft no-op, got error: %v", err)
	}
}
