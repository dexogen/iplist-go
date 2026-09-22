package app

import (
	"reflect"
	"testing"
)

func TestDNSAdditionsExcludeLocalAndBlockingAddresses(t *testing.T) {
	addresses := []string{"8.8.8.8", "2606:4700:4700::1111", "0.0.0.0", "127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.20.1", "169.254.1.1", "100.64.0.1", "224.0.0.1", "255.255.255.255", "::", "::1", "fc00::1", "fe80::1", "ff02::1", "::ffff:192.168.20.1"}
	want := []string{"2606:4700:4700::1111", "8.8.8.8"}
	if got := filterAdditionalIPs(nil, nil, addresses); !reflect.DeepEqual(got, want) {
		t.Fatalf("DNS additions = %v, want %v", got, want)
	}
}
