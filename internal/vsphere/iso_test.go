package vsphere

import (
	"os"
	"path/filepath"
	"testing"
)

func TestISOFileNameDistinctURLs(t *testing.T) {
	a := "https://assisted.example/images/4.20/x86_64/minimal.iso?api_key=one"
	b := "https://assisted.example/images/4.20/x86_64/minimal.iso?api_key=two"
	c := "https://assisted.example/images/4.19/x86_64/rhcos.iso"
	if ISOFileName(a) == ISOFileName(b) {
		t.Fatalf("different URLs reused one cache name: %s", ISOFileName(a))
	}
	if ISOFileName(a) == ISOFileName(c) || ISOFileName(b) == ISOFileName(c) {
		t.Fatal("unrelated image URLs reused one cache name")
	}
	if got := ISOFileName(a); got != ISOFileName(a) {
		t.Fatalf("cache name not stable: %s", got)
	}
}

func TestContentISONameFollowsBytes(t *testing.T) {
	dir := t.TempDir()
	one := filepath.Join(dir, "one.iso")
	two := filepath.Join(dir, "two.iso")
	same := filepath.Join(dir, "same.iso")
	if err := os.WriteFile(one, []byte("minimal-iso-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(two, []byte("install-iso-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(same, []byte("minimal-iso-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	n1, err := contentISOName(one)
	if err != nil {
		t.Fatal(err)
	}
	n2, err := contentISOName(two)
	if err != nil {
		t.Fatal(err)
	}
	n3, err := contentISOName(same)
	if err != nil {
		t.Fatal(err)
	}
	if n1 == n2 {
		t.Fatalf("different ISO bytes produced %s", n1)
	}
	if n1 != n3 {
		t.Fatalf("identical ISO bytes produced %s and %s", n1, n3)
	}
}

func TestISOPathsEqual(t *testing.T) {
	const path = "[Qumulo] rf2vc/isos/abcd.iso"
	if !isoPathsEqual(path, "[qumulo]  rf2vc/isos/abcd.iso") {
		t.Fatal("expected datastore path match ignoring case and spacing")
	}
	if isoPathsEqual(path, "[Qumulo] rf2vc/isos/other.iso") {
		t.Fatal("different ISO files compared equal")
	}
	if isoPathsEqual("", "") || isoPathsEqual(path, "") {
		t.Fatal("empty path compared equal")
	}
}
