package runtime

import (
	"context"
	"strings"
	"testing"
)

// THE RULE: when the daemon refuses a deploy, the user reads the daemon's reason.
//
// The wizard prints the rejected DeployService call as is (String(error) in
// runWizardAction), and nothing between ContainerCreate/ContainerStart and that
// call rewrites the error. So if Deploy itself returned a generic "deploy failed",
// a port another container already holds would be a mystery to the user. These
// cases pin Docker's own text, on one line, at the point Deploy returns it.
func TestDeployRefusalCarriesTheDaemonsReason(t *testing.T) {
	cases := []struct {
		name          string
		createRefusal string
		startRefusal  string
		wantRemoved   bool
	}{
		{
			name:         "host port already allocated",
			startRefusal: "failed to set up container networking: driver failed programming external connectivity on endpoint cashpilot-orderly (abc123): Bind for 0.0.0.0:28967 failed: port is already allocated",
			wantRemoved:  true,
		},
		{
			name:         "device missing on the host",
			startRefusal: `error gathering device information while adding custom device "/dev/net/tun": no such file or directory`,
			wantRemoved:  true,
		},
		{
			name:          "create refused",
			createRefusal: `Conflict. The container name "/cashpilot-orderly" is already in use by container "f00d". You have to remove (or rename) that container to be able to reuse that name.`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeDocker{createRefusal: tc.createRefusal, startRefusal: tc.startRefusal}
			fake.start(t)

			_, err := (&DockerProvider{}).Deploy(context.Background(), deploySpec("orderly"), nil)
			if err == nil {
				t.Fatal("the deploy succeeded although the daemon refused it")
			}
			reason := tc.startRefusal + tc.createRefusal
			msg := err.Error()
			if !strings.Contains(msg, reason) {
				t.Errorf("the error the user sees is %q; it lost the daemon's reason %q", msg, reason)
			}
			if strings.ContainsAny(msg, "\r\n") {
				t.Errorf("the error spans several lines: %q", msg)
			}
			// A start that failed must not leave its created container behind.
			removed := false
			for _, call := range fake.recorded() {
				if call == "DELETE /containers/deadbeef" {
					removed = true
				}
			}
			if removed != tc.wantRemoved {
				t.Errorf("removed the created container = %v, want %v (calls: %v)", removed, tc.wantRemoved, fake.recorded())
			}
		})
	}
}
