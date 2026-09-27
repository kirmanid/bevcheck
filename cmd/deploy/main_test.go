package main

import "testing"

func TestSSHCIDRForIngressFailClosed(t *testing.T) {
	if _, err := sshCIDRForIngress(""); err == nil {
		t.Fatalf("empty IP must fail closed")
	}
	if _, err := sshCIDRForIngress("   "); err == nil {
		t.Fatalf("whitespace IP must fail closed")
	}
	if cidr, err := sshCIDRForIngress("1.2.3.4"); err != nil || cidr != "1.2.3.4/32" {
		t.Fatalf("got %q, %v; want 1.2.3.4/32", cidr, err)
	}
}
