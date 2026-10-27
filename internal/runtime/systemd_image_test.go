package runtime

import "testing"

// THE RULE: an image that boots an init system (systemd, nearly always) as PID 1 is refused before the running
// container is touched. Under the capability drop in buildHostConfig such an image
// exits 255 at once, with nothing in the logs, and the restart policy loops it
// forever; a user who typed a community image's full tag in the Image box saw
// exactly that. Only the first word of what finally runs decides, and the catalog's
// entrypoint replaces the image's while its command replaces only the image CMD.
func TestBootsInitSystem(t *testing.T) {
	cases := []struct {
		name                      string
		imageEntrypoint, imageCmd []string
		entrypoint, cmd           []string
		want                      bool
	}{
		{"ubuntu systemd image", nil, []string{"/sbin/init"}, nil, nil, true},
		{"systemd binary as entrypoint", []string{"/lib/systemd/systemd"}, nil, nil, nil, true},
		{"usr-merged init", nil, []string{"/usr/sbin/init"}, nil, nil, true},
		{"lite image", []string{"entrypoint"}, nil, nil, nil, false},
		{"image entrypoint wins over its cmd", []string{"entrypoint"}, []string{"/sbin/init"}, nil, nil, false},
		{"catalog entrypoint replaces the image's", nil, []string{"/sbin/init"}, []string{"/run.sh"}, nil, false},
		{"catalog command replaces the cmd", nil, []string{"/sbin/init"}, nil, []string{"earnapp", "run"}, false},
		{"catalog command does not replace the entrypoint", []string{"/sbin/init"}, nil, nil, []string{"earnapp", "run"}, true},
		{"empty image", nil, nil, nil, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := bootsInitSystem(tc.imageEntrypoint, tc.imageCmd, tc.entrypoint, tc.cmd); got != tc.want {
				t.Fatalf("bootsInitSystem = %v, want %v", got, tc.want)
			}
		})
	}
}
