package harness

import (
	"net/http"
	"strings"
	"testing"
)

func call(t *testing.T, d *Docker, method, path, body string) int {
	t.Helper()
	req, err := http.NewRequest(method, d.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// A deleted build is gone, as on Docker: an inspect or a create of it answers
// that the image is missing, until a pull brings it back.
func TestADeletedImageIsGoneUntilItIsPulledAgain(t *testing.T) {
	d := NewDocker()
	defer d.Close()
	d.SetImage("example/app:latest", "sha256:one", "sha256:d1")
	if code := call(t, d, http.MethodGet, "/images/example/app:latest/json", ""); code != http.StatusOK {
		t.Fatalf("inspect before the delete = %d", code)
	}
	if code := call(t, d, http.MethodDelete, "/images/sha256:one", ""); code != http.StatusOK {
		t.Fatalf("delete = %d", code)
	}
	for _, ref := range []string{"sha256:one", "example/app:latest"} {
		if code := call(t, d, http.MethodGet, "/images/"+ref+"/json", ""); code != http.StatusNotFound {
			t.Fatalf("inspect of %s after the delete = %d; want 404", ref, code)
		}
	}
	if code := call(t, d, http.MethodPost, "/containers/create?name=a", `{"Image":"example/app:latest"}`); code != http.StatusNotFound {
		t.Fatalf("create from a deleted image = %d; want 404", code)
	}
	// Control: another image is still there, and a pull brings the build back.
	if code := call(t, d, http.MethodGet, "/images/example/other:1/json", ""); code != http.StatusOK {
		t.Fatalf("inspect of another image = %d", code)
	}
	if code := call(t, d, http.MethodPost, "/images/create?fromImage=example/app&tag=latest", ""); code != http.StatusOK {
		t.Fatalf("pull = %d", code)
	}
	if code := call(t, d, http.MethodGet, "/images/sha256:one/json", ""); code != http.StatusOK {
		t.Fatalf("inspect after the pull = %d; want 200", code)
	}
}

// The default build of a tag, one SetImage never named, is deleted and pulled
// back the same way.
func TestADeletedDefaultBuildIsGoneUntilItIsPulledAgain(t *testing.T) {
	d := NewDocker()
	defer d.Close()
	if code := call(t, d, http.MethodDelete, "/images/sha256:example/app:latest", ""); code != http.StatusOK {
		t.Fatalf("delete = %d", code)
	}
	if code := call(t, d, http.MethodGet, "/images/example/app:latest/json", ""); code != http.StatusNotFound {
		t.Fatalf("inspect of the tag after the delete = %d; want 404", code)
	}
	if code := call(t, d, http.MethodPost, "/containers/create?name=a", `{"Image":"example/app:latest"}`); code != http.StatusNotFound {
		t.Fatalf("create from a deleted image = %d; want 404", code)
	}
	if code := call(t, d, http.MethodPost, "/images/create?fromImage=example/app&tag=latest", ""); code != http.StatusOK {
		t.Fatalf("pull = %d", code)
	}
	if code := call(t, d, http.MethodGet, "/images/example/app:latest/json", ""); code != http.StatusOK {
		t.Fatalf("inspect after the pull = %d; want 200", code)
	}
}

// A bind at a path the image declares as a VOLUME is that path's mount: Docker
// makes no anonymous volume there.
func TestABindAtAnImageVolumePathGetsNoAnonymousVolume(t *testing.T) {
	d := NewDocker()
	defer d.Close()
	d.SetImage("example/app:latest", "sha256:one", "")
	d.SetImageVolumes("sha256:one", "/data", "/cache")
	body := `{"Image":"example/app:latest","HostConfig":{"Mounts":[{"Type":"bind","Source":"/host/data","Target":"/data"}]}}`
	if code := call(t, d, http.MethodPost, "/containers/create?name=a", body); code != http.StatusCreated {
		t.Fatalf("create = %d", code)
	}
	c, _ := d.Container("a")
	var anonymous []string
	for _, m := range c.Mounts {
		if m.Anonymous {
			anonymous = append(anonymous, m.Target)
		}
	}
	if len(anonymous) != 1 || anonymous[0] != "/cache" {
		t.Fatalf("anonymous volumes at %v; want /cache only", anonymous)
	}
}
