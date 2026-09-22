package redfish

import "testing"

func TestRewriteBareUUIDPath(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"/", "", false},
		{"/Systems/abc", "", false},
		{"/42394b25-7a2c-c177-03f4-2bb3e5d147cb", "/Systems/42394b25-7a2c-c177-03f4-2bb3e5d147cb", true},
		{"/42394b25-7a2c-c177-03f4-2bb3e5d147cb/Actions/ComputerSystem.Reset", "/Systems/42394b25-7a2c-c177-03f4-2bb3e5d147cb/Actions/ComputerSystem.Reset", true},
		{"/Managers/1", "", false},
		{"/not-a-uuid", "", false},
	}
	for _, tc := range cases {
		got, ok := rewriteBareUUIDPath(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("%q: got (%q,%v) want (%q,%v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
