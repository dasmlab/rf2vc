package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUUIDMapPersistence(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	vc, err := s.UpsertVCenter(VCenter{
		Name: "lab", URL: "https://vc.example", Username: "u", Password: "p",
		Datacenter: "DC1", Datastore: "ds1", Insecure: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if vc.Password != "" {
		t.Fatal("password should be redacted on return")
	}
	m, err := s.UpsertMapping(Mapping{UUID: "AA-BB", VCenterID: vc.ID, Name: "node1"})
	if err != nil {
		t.Fatal(err)
	}
	if m.UUID != "aa-bb" {
		t.Fatalf("uuid normalize: %q", m.UUID)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, vc2, ok := s2.LookupUUID("aa-bb")
	if !ok || got.Name != "node1" || vc2.URL != "https://vc.example" {
		t.Fatalf("lookup after reload: ok=%v got=%+v vc=%+v", ok, got, vc2)
	}
	secret, ok := s2.GetVCenterSecret(vc.ID)
	if !ok || secret.Password != "p" {
		t.Fatalf("secret password lost: %+v", secret)
	}
	if _, err := os.Stat(filepath.Join(dir, "state.json")); err != nil {
		t.Fatal(err)
	}
}
