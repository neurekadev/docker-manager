package migrations

import "testing"

func TestOldSubscriptionsKeepWhatAChannelSent(t *testing.T) {
	for _, c := range []struct {
		kinds    string
		resolved bool
		want     string
	}{
		// A channel with every old kind (the old default) gets the new
		// kinds too.
		{`["disk_health","raid","environment_offline","job_failed","updates_available"]`, true,
			`{"backup":["failure","warning","success"],"disk_health":["warning","critical","resolved"],` +
				`"disk_space":["warning","critical","resolved"],"environment_offline":["critical","resolved"],` +
				`"job_failed":["failure","warning","resolved"],"memory":["warning","critical","resolved"],` +
				`"prune":["failure","success"],"raid":["warning","critical","resolved"],` +
				`"temperature":["warning","critical","resolved"],"updates":["available","failure","success"]}`},
		// Some kinds without resolved messages: those kinds, no resolutions.
		{`["raid","updates_available"]`, false, `{"raid":["warning","critical"],"updates":["available"]}`},
		{`["environment_offline"]`, false, `{"environment_offline":["critical"]}`},
	} {
		got, err := oldSubscriptions(c.kinds, c.resolved)
		if err != nil || got != c.want {
			t.Errorf("%s %v:\n%s\nwant\n%s (%v)", c.kinds, c.resolved, got, c.want, err)
		}
	}
	if _, err := oldSubscriptions("not json", true); err == nil {
		t.Error("broken kinds accepted")
	}
}
