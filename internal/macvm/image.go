package macvm

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Progress reports a long step: what runs, and how far, 0 to 1 (-1 unknown).
type Progress func(step string, fraction float64)

// Download fetches pin to dest, resuming a partial file, and keeps it only if
// it hashes to the pin. A file already at dest with the right hash is kept.
func Download(ctx context.Context, client *http.Client, pin Pin, dest string, progress Progress) error {
	if ok, _ := fileHashIs(dest, pin.SHA256); ok {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	part := dest + ".part"
	h := sha256.New()
	var have int64
	if f, err := os.Open(part); err == nil {
		n, err := io.Copy(h, f)
		f.Close()
		if err != nil {
			return err
		}
		have = n
		// A run that stopped between the last byte and the rename left the
		// whole file: asking for more would get 416 every time.
		if have > 0 && hex.EncodeToString(h.Sum(nil)) == pin.SHA256 {
			return os.Rename(part, dest)
		}
		// A full-size partial with the wrong hash can never become right.
		if pin.Size > 0 && have >= pin.Size {
			if err := os.Remove(part); err != nil {
				return err
			}
			have = 0
			h.Reset()
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pin.URL, nil)
	if err != nil {
		return err
	}
	if have > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(have, 10)+"-")
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("could not download %s: %w", pin.Name, err)
	}
	defer resp.Body.Close()
	flags := os.O_CREATE | os.O_WRONLY
	switch {
	case resp.StatusCode == http.StatusPartialContent && have > 0:
		flags |= os.O_APPEND
	case resp.StatusCode == http.StatusOK:
		flags |= os.O_TRUNC
		have = 0
		h.Reset()
	default:
		return fmt.Errorf("could not download %s: the server answered %s", pin.Name, resp.Status)
	}
	total := pin.Size
	if total == 0 && resp.ContentLength > 0 {
		total = have + resp.ContentLength
	}
	f, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return err
	}
	w := io.MultiWriter(f, h)
	buf := make([]byte, 1<<20)
	last := time.Time{}
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := w.Write(buf[:n]); err != nil {
				f.Close()
				return err
			}
			have += int64(n)
			if progress != nil && time.Since(last) > 500*time.Millisecond {
				last = time.Now()
				frac := -1.0
				if total > 0 {
					frac = float64(have) / float64(total)
				}
				progress("download:"+pin.Name, frac)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			return fmt.Errorf("the download of %s stopped: %w (it resumes next time)", pin.Name, rerr)
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != pin.SHA256 {
		os.Remove(part)
		return fmt.Errorf("the download of %s does not match its pinned checksum, so it was thrown away", pin.Name)
	}
	return os.Rename(part, dest)
}

func fileHashIs(path, want string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}
	return hex.EncodeToString(h.Sum(nil)) == want, nil
}

// Extract unpacks a pinned .tar.gz into dir with the system tar, which
// refuses absolute and ".." paths. The archive was hash-checked first.
func Extract(ctx context.Context, archive, dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	_, err := Exec{}.Run(ctx, "/usr/bin/tar", "-xzf", archive, "-C", dir)
	return err
}

// Tools is where the owner-side download keeps the unpacked tools.
type Tools struct {
	Dir     string // ~/Library/Caches/CashPilot/securevm
	Tart    string // Dir/tart/tart.app/Contents/MacOS/tart
	TartApp string
	Softnet string
	Agent   string
}

// ToolsAt lays the tool cache out under dir.
func ToolsAt(dir string) Tools {
	return Tools{
		Dir:     dir,
		TartApp: filepath.Join(dir, "tart", TartApp),
		Tart:    TartExe(filepath.Join(dir, "tart")),
		Softnet: filepath.Join(dir, "softnet", SoftnetExe),
		Agent:   filepath.Join(dir, "agent", "tart-guest-agent"),
	}
}

// FetchTools downloads and unpacks Tart, softnet and the guest agent.
func FetchTools(ctx context.Context, client *http.Client, t Tools, progress Progress) error {
	for _, x := range []struct {
		pin Pin
		sub string
	}{{TartPin, "tart"}, {SoftnetPin, "softnet"}, {AgentPin, "agent"}} {
		archive := filepath.Join(t.Dir, x.sub+".tar.gz")
		if err := Download(ctx, client, x.pin, archive, progress); err != nil {
			return err
		}
		if err := Extract(ctx, archive, filepath.Join(t.Dir, x.sub)); err != nil {
			return fmt.Errorf("could not unpack %s: %w", x.pin.Name, err)
		}
	}
	for _, p := range []string{t.Tart, t.Softnet, t.Agent} {
		if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
			return fmt.Errorf("%s is missing from its release", filepath.Base(p))
		}
	}
	return nil
}

// IPSWPath is where the restore image waits: beside the VM, on the volume
// that will hold it.
func IPSWPath(tartHome string) string {
	return filepath.Join(tartHome, "cache", "IPSWs", filepath.Base(IPSWPin.URL))
}

// CreateVM installs macOS from the restore image into a new VM and sizes it.
// It runs as the owner. Tart prints the install's percentage as it goes.
func CreateVM(ctx context.Context, tart, tartHome, ipsw string, progress Progress) error {
	cmd := exec.CommandContext(ctx, tart, "create", "--from-ipsw", ipsw, "--disk-size", strconv.Itoa(GuestDiskGB), VMName)
	cmd.Env = append(os.Environ(), "TART_HOME="+tartHome)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var errb strings.Builder
	cmd.Stderr = &errb
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(out)
	var tail []string
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if p, ok := strings.CutSuffix(l, "%"); ok {
			if n, err := strconv.Atoi(p); err == nil && progress != nil {
				progress("install-macos", float64(n)/100)
			}
			continue
		}
		tail = append(tail, l)
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("macOS did not install into the VM: %v %s %s", err, strings.Join(tail, " "), strings.TrimSpace(errb.String()))
	}
	set := exec.CommandContext(ctx, tart, "set", VMName, "--cpu", strconv.Itoa(GuestCPUs), "--memory", strconv.Itoa(GuestMemoryMB))
	set.Env = cmd.Env
	if b, err := set.CombinedOutput(); err != nil {
		return fmt.Errorf("could not size the VM: %v %s", err, b)
	}
	return nil
}

// VMConfig is the part of Tart's config.json we read.
type VMConfig struct {
	MAC  string `json:"macAddress"`
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

// ReadVMConfig reads the VM's config.json.
func ReadVMConfig(tartHome string) (VMConfig, error) {
	var c VMConfig
	if err := ReadJSON(filepath.Join(VMDir(tartHome), "config.json"), &c); err != nil {
		return c, err
	}
	if _, ok := NormalizeMAC(c.MAC); !ok || c.OS != "darwin" {
		return c, errors.New("the VM's config.json is not a macOS VM's")
	}
	return c, nil
}

// guestAgentPlist starts the guest agent as root at every guest boot, with
// its RPC part only: no clipboard channel (lab, M2).
const guestAgentPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>org.cashpilot.guest-agent</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/libexec/tart-guest-agent</string>
    <string>--run-rpc</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardErrorPath</key><string>/var/log/cashpilot-guest-agent.log</string>
</dict>
</plist>
`

// Inject writes the guest agent into the stopped VM's disk, before the guest
// ever boots: the agent binary, its LaunchDaemon, and the marker that skips
// Setup Assistant. So the guest needs no Setup Assistant, no admin account
// and no SSH; from its first boot it is reached only through `tart exec`.
// It runs as root, from the admin step.
func Inject(ctx context.Context, r Runner, disk, agent string) (err error) {
	out, err := r.Run(ctx, "/usr/bin/hdiutil", "attach", "-imagekey", "diskimage-class=CRawDiskImage", "-nomount", "-noverify", "-plist", disk)
	if err != nil {
		return fmt.Errorf("could not open the VM's disk: %w", err)
	}
	whole, slices, err := attachedDisk(ctx, out)
	if whole != "" {
		// The disk is attached even when it holds no macOS volume, so the
		// detach covers that error too.
		// A disk that stays attached must stop the install: the guard would
		// start a VM on a disk the host still holds.
		defer func() {
			if _, derr := r.Run(context.WithoutCancel(ctx), "/usr/bin/hdiutil", "detach", whole); derr != nil {
				if _, ferr := r.Run(context.WithoutCancel(ctx), "/usr/bin/hdiutil", "detach", "-force", whole); ferr != nil && err == nil {
					err = fmt.Errorf("could not release the VM's disk: %w", ferr)
				}
			}
		}()
	}
	if err != nil {
		return err
	}
	vol, err := dataVolume(ctx, r, slices)
	if err != nil {
		return err
	}
	mnt, err := os.MkdirTemp("/private/var/run", "cashpilot-vmdisk.")
	if err != nil {
		return err
	}
	defer os.Remove(mnt)
	if _, err := r.Run(ctx, "/sbin/mount_apfs", "-o", "nobrowse,nosuid,nodev", "/dev/"+vol, mnt); err != nil {
		return fmt.Errorf("could not mount the VM's data volume: %w", err)
	}
	defer func() {
		_, _ = r.Run(context.WithoutCancel(ctx), "/sbin/umount", mnt)
	}()
	agentBytes, err := os.ReadFile(agent)
	if err != nil {
		return err
	}
	if err := SafeWrite(mnt, "usr/local/libexec/tart-guest-agent", agentBytes, 0o755); err != nil {
		return err
	}
	if err := SafeWrite(mnt, "Library/LaunchDaemons/org.cashpilot.guest-agent.plist", []byte(guestAgentPlist), 0o644); err != nil {
		return err
	}
	return SafeWrite(mnt, "private/var/db/.AppleSetupDone", nil, 0o644)
}

// attachedDisk reads `hdiutil attach -plist`: the whole disk and its slices.
func attachedDisk(ctx context.Context, plist string) (string, []string, error) {
	js, err := plistJSON(ctx, plist)
	if err != nil {
		return "", nil, err
	}
	var a struct {
		Entities []struct {
			Dev     string `json:"dev-entry"`
			Content string `json:"content-hint"`
		} `json:"system-entities"`
	}
	if err := json.Unmarshal(js, &a); err != nil {
		return "", nil, err
	}
	whole := ""
	var slices []string
	for _, e := range a.Entities {
		d := strings.TrimPrefix(e.Dev, "/dev/")
		if e.Content == "GUID_partition_scheme" {
			whole = d
		} else if e.Content == "Apple_APFS" {
			slices = append(slices, d)
		}
	}
	if whole == "" || len(slices) == 0 {
		return whole, nil, errors.New("the VM's disk holds no macOS volume")
	}
	return whole, slices, nil
}

// dataVolume finds the volume with the Data role in the container on slices.
func dataVolume(ctx context.Context, r Runner, slices []string) (string, error) {
	out, err := r.Run(ctx, "/usr/sbin/diskutil", "apfs", "list", "-plist")
	if err != nil {
		return "", err
	}
	js, err := plistJSON(ctx, out)
	if err != nil {
		return "", err
	}
	var l struct {
		Containers []struct {
			PhysicalStores []struct {
				DeviceIdentifier string
			}
			Volumes []struct {
				DeviceIdentifier string
				Roles            []string
			}
		}
	}
	if err := json.Unmarshal(js, &l); err != nil {
		return "", err
	}
	for _, c := range l.Containers {
		ours := false
		for _, ps := range c.PhysicalStores {
			for _, s := range slices {
				if ps.DeviceIdentifier == s {
					ours = true
				}
			}
		}
		if !ours {
			continue
		}
		for _, v := range c.Volumes {
			for _, role := range v.Roles {
				if role == "Data" {
					return v.DeviceIdentifier, nil
				}
			}
		}
	}
	return "", errors.New("the VM's disk has no Data volume; macOS did not finish installing")
}

// plistJSON converts a property list to JSON with plutil.
func plistJSON(ctx context.Context, plist string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "/usr/bin/plutil", "-convert", "json", "-o", "-", "-")
	cmd.Stdin = strings.NewReader(plist)
	return cmd.Output()
}
