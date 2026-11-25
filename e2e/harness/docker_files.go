package harness

import (
	"archive/tar"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"path"
	"strings"
)

// Files in named volumes, and the archive API that reads and writes them: what a
// deploy uses to put a wallet seed file into a service's data volume through a
// container that is created and never started (internal/runtime/seedfile.go).
// Files written into a container's own filesystem, outside any named volume, are
// not kept: nothing the app does needs them.

// VolumeFile is one file the fake holds in a named volume.
type VolumeFile struct {
	Data []byte
	Mode int64
	UID  int
	GID  int
}

// VolumeFile returns the file at rel (relative to the volume's root) in volume.
func (d *Docker) VolumeFile(volume, rel string) (VolumeFile, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f, ok := d.volumeFiles[volume][strings.TrimPrefix(rel, "/")]
	if !ok {
		return VolumeFile{}, false
	}
	f.Data = append([]byte(nil), f.Data...)
	return f, true
}

// PutVolumeFile puts a file into volume, creating the volume, as something that ran
// earlier would have left it.
func (d *Docker) PutVolumeFile(volume, rel string, data []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.volumes[volume] = true
	d.putFile(volume, rel, VolumeFile{Data: append([]byte(nil), data...), Mode: 0o644})
}

// Created returns every container ever created, in order, as its create asked for
// it: the short-lived helpers a deploy removes again included.
func (d *Docker) Created() []Container {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]Container(nil), d.created...)
}

// SetGeneratorOutput is what a container labelled cashpilot.wallet-helper=generate
// prints on stdout, as an image's own wallet generator would.
func (d *Docker) SetGeneratorOutput(text string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.generatorOutput = text
}

// putFile stores a file. The caller holds the lock.
func (d *Docker) putFile(volume, rel string, f VolumeFile) {
	if d.volumeFiles == nil {
		d.volumeFiles = map[string]map[string]VolumeFile{}
	}
	if d.volumeFiles[volume] == nil {
		d.volumeFiles[volume] = map[string]VolumeFile{}
	}
	d.volumeFiles[volume][strings.TrimPrefix(rel, "/")] = f
}

// volumeAt is the named volume c mounts at or above p, and p relative to its root.
// The caller holds the lock.
func (c *Container) volumeAt(p string) (string, string, bool) {
	p = path.Clean(p)
	for _, m := range c.Mounts {
		target := path.Clean(m.Target)
		if p == target {
			return m.Source, "", true
		}
		if strings.HasPrefix(p, target+"/") {
			return m.Source, strings.TrimPrefix(p, target+"/"), true
		}
	}
	return "", "", false
}

// archive answers PUT and GET /containers/<id>/archive?path=...
func (d *Docker) archive(w http.ResponseWriter, r *http.Request, id string) {
	target := r.URL.Query().Get("path")
	d.mu.Lock()
	defer d.mu.Unlock()
	c := d.find(id)
	if c == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "No such container: " + id})
		return
	}
	switch r.Method {
	case http.MethodPut:
		volume, base, ok := c.volumeAt(target)
		tr := tar.NewReader(r.Body)
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"message": "fake daemon: unreadable archive"})
				return
			}
			if hdr.Typeflag != tar.TypeReg || !ok {
				continue
			}
			data, _ := io.ReadAll(tr)
			d.putFile(volume, path.Join(base, hdr.Name), VolumeFile{Data: data, Mode: hdr.Mode, UID: hdr.Uid, GID: hdr.Gid})
		}
		w.WriteHeader(http.StatusOK)
	case http.MethodGet, http.MethodHead:
		volume, rel, ok := c.volumeAt(target)
		f, found := VolumeFile{}, false
		if ok {
			f, found = d.volumeFiles[volume][rel]
		}
		if !found {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "Could not find the file " + target + " in container " + id})
			return
		}
		stat, _ := json.Marshal(map[string]any{"name": path.Base(target), "size": len(f.Data), "mode": f.Mode})
		w.Header().Set("X-Docker-Container-Path-Stat", base64.StdEncoding.EncodeToString(stat))
		w.Header().Set("Content-Type", "application/x-tar")
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		_ = tw.WriteHeader(&tar.Header{Name: path.Base(target), Mode: f.Mode, Size: int64(len(f.Data)), Uid: f.UID, Gid: f.GID, Typeflag: tar.TypeReg})
		_, _ = tw.Write(f.Data)
		_ = tw.Close()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(buf.Bytes())
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "fake daemon: archive " + r.Method})
	}
}
