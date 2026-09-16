package vsphere

import "testing"

func TestNormalizeInventoryPath(t *testing.T) {
	cases := map[string]string{
		"/Montreal/vm/VMs/TDM/OpenShift/ACM/LAB": "/montreal/vm/vms/tdm/openshift/acm/lab",
		"/Montreal/vm/VMs/TDM/":                  "/montreal/vm/vms/tdm",
		`\Montreal\vm\LAB`:                       "/montreal/vm/lab",
	}
	for in, want := range cases {
		got := normalizeInventoryPath(in)
		if got != want {
			t.Errorf("normalizeInventoryPath(%q)=%q want %q", in, got, want)
		}
	}
}
