package cli

import (
	"reflect"
	"testing"
	"time"
)

// completionSlack is the most a bounded completion may take: the configured
// budget (PVE_CLI_COMPLETION_TIMEOUT) plus slack for CI scheduling and -race.
func completionSlack(t *testing.T) time.Duration {
	t.Helper()
	return completionTimeout() + 600*time.Millisecond
}

// --vmid takes numeric ids, so its completion offers ids only (also inside a
// comma list); positional guest args still offer names.
func TestCompleteVMIDFlagIDsOnly(t *testing.T) {
	isolateCache(t)
	srv := completionServer(t, nil)
	cases := []struct {
		args []string
		want []string
	}{
		{[]string{"firewall", "rules", "--vmid", ""}, []string{"100\tweb-01", "101\tdb-01", "200\tdns"}},
		{[]string{"tag", "add", "--vmid", "100,"}, []string{"100,100\tweb-01", "100,101\tdb-01", "100,200\tdns"}},
		{[]string{"tag", "add", "--vmid", "we"}, nil},
		{[]string{"snapshot", "list", "--vmid", "d"}, nil},
		{[]string{"vm", "start", "we"}, []string{"web-01\tvmid 100"}},
	}
	for _, c := range cases {
		if got := complete(t, withCreds(srv, c.args...)...); !reflect.DeepEqual(got, c.want) {
			t.Errorf("__complete %v\n got %q\nwant %q", c.args, got, c.want)
		}
	}
}
