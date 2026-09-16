package vsphere

import "testing"

func TestNormalizeDatastore(t *testing.T) {
	cases := map[string]string{
		"":        "",
		"NONE":    "",
		"none":    "",
		"notset":  "",
		"Not Set": "",
		"n/a":     "",
		"-":       "",
		"vsanDatastore": "vsanDatastore",
		" DS01 ":  "DS01",
	}
	for in, want := range cases {
		if got := NormalizeDatastore(in); got != want {
			t.Errorf("NormalizeDatastore(%q)=%q want %q", in, got, want)
		}
	}
}

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

func TestFolderPathCandidates(t *testing.T) {
	got := folderPathCandidates("/Montreal/vm/VMs/TDM/OpenShift/ACM/")
	if len(got) < 1 {
		t.Fatalf("expected candidates, got %v", got)
	}
	if got[0] != "/Montreal/vm/VMs/TDM/OpenShift/ACM" {
		t.Errorf("first=%q", got[0])
	}
	// Must include /vm/ normalization of /VMs/
	wantAlt := "/Montreal/vm/vm/TDM/OpenShift/ACM"
	found := false
	for _, p := range got {
		if p == wantAlt {
			found = true
		}
	}
	if !found {
		t.Errorf("expected alt %q in %v", wantAlt, got)
	}
}
