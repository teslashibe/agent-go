package ssh

import (
	"context"
	"testing"
)

func TestRejectShellSyntax(t *testing.T) {
	for _, tc := range []struct{ host, path string }{
		{"-oProxyCommand=bad", "/usr/bin/imsg"},
		{"mac;bad", "/usr/bin/imsg"},
		{"mac", "/tmp/imsg;bad"},
		{"mac", "imsg"},
		{"", "/usr/bin/imsg"},
	} {
		if _, err := Open(context.Background(), tc.host, tc.path); err == nil {
			t.Fatalf("accepted %+v", tc)
		}
	}
}
