package runtime

// App image updates (internal/imageupdates): the runtime half.
//
// Desktop pins every live catalog image by digest (catalog-overlay/image-pins.yml),
// and a deploy pulls exactly that build. The pin is the known-good build. With
// image updates on, a service follows the tag its pin names instead: CheckImage
// pulls that tag and says whether its build differs from the running one, and a
// deploy with FollowTag runs the tag's build. A deploy with SettleSeconds watches
// the new container that long and puts the old one back when it fails.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/i18n"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// settlePoll is how often a deploy with a check period looks at the new container.
// A variable so tests can make it short.
var settlePoll = 5 * time.Second

// settleGrace is how long a check period runs on when Docker cannot describe the
// new container at its end: passing needs a look that worked. A variable for tests.
var settleGrace = 30 * time.Second

// settleNow is the clock a check period reads, a variable so a test can make one
// look take long.
var settleNow = time.Now

// settleLookTimeout bounds one look at the new container: a daemon that takes
// a request and never answers counts as no answer, so the period still ends.
var settleLookTimeout = 30 * time.Second

// imageRemoveTimeout bounds the delete of the old image after a pass.
var imageRemoveTimeout = 2 * time.Minute

// settleLineBytes caps one log line a check period keeps while it waits for the
// line's end. A pattern can span a whole line, so a longer line cannot be
// judged in parts: the log reads as unreadable, and unknown is not clean.
const settleLineBytes = 1 << 20

// FollowRef is the reference an image update checks and deploys for svc: the tag
// that its image names, without the digest. ok is false for an image that names
// no tag to follow: a custom image pinned by digest alone. A catalog image with a
// digest and no tag follows "latest", which is what the web catalog runs.
func FollowRef(svc catalog.Service, arch string) (ref string, ok bool) {
	image := ImageForArch(svc.Docker, arch)
	if image == "" {
		return "", false
	}
	_, digest, _ := strings.Cut(image, "@")
	// With the digest: splitRef says "" for a digest with no tag, "latest" only
	// for a plain repository.
	repo, tag := splitRef(image)
	if digest != "" && tag == "" {
		if svc.Custom {
			return "", false
		}
		tag = "latest"
	}
	if tag == "" {
		tag = "latest"
	}
	return repo + ":" + tag, true
}

// ImageCheck is what CheckImage found for the first copy of a service.
type ImageCheck struct {
	// Ref is the tag that was pulled; Floating is false when there is none to follow.
	Ref      string
	Floating bool
	// LatestID and LatestDigest are the tag's build after the pull.
	LatestID     string
	LatestDigest string
	// RunningID and RunningDigest are the build the container runs.
	RunningID     string
	RunningDigest string
	ContainerID   string
	// Status is the container's state: running, exited, restarting...
	Status string
}

// ErrNoContainer is CheckImage finding no container for the service.
var ErrNoContainer = errors.New("no container")

// CheckImage pulls the tag that slug's image follows and compares its build with
// the one the container runs. It changes no container.
func (p *DockerProvider) CheckImage(ctx context.Context, slug string, svc catalog.Service) (ImageCheck, error) {
	cli, err := dockerClient()
	if err != nil {
		return ImageCheck{}, err
	}
	defer cli.Close()
	name := containerName(slug)
	got, err := cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return ImageCheck{}, ErrNoContainer
	}
	if err != nil {
		return ImageCheck{}, err
	}
	if err := desktopOwned(name, got.Container); err != nil {
		return ImageCheck{}, err
	}
	ref, ok := FollowRef(svc, daemonFacts(ctx, cli).Architecture)
	check := ImageCheck{Ref: ref, Floating: ok, ContainerID: got.Container.ID, RunningID: got.Container.Image}
	if got.Container.State != nil {
		check.Status = string(got.Container.State.Status)
	}
	if !ok {
		return check, nil
	}
	if err := pullImage(ctx, cli, ref, nil); err != nil {
		return ImageCheck{}, err
	}
	latest, err := cli.ImageInspect(ctx, ref)
	if err != nil {
		return ImageCheck{}, err
	}
	check.LatestID, check.LatestDigest = latest.ID, repoDigest(latest.RepoDigests, ref)
	if running, err := cli.ImageInspect(ctx, check.RunningID); err == nil {
		check.RunningDigest = repoDigest(running.RepoDigests, ref)
	}
	return check, nil
}

// repoDigest is the digest of ref's repository among an image's repo digests.
func repoDigest(digests []string, ref string) string {
	repo, _ := splitRef(ref)
	want := canonicalRepo(repo)
	for _, d := range digests {
		if r, digest, ok := strings.Cut(d, "@"); ok && canonicalRepo(r) == want {
			return digest
		}
	}
	// Another repository's digest is not this one's: no digest beats a wrong one.
	return ""
}

// canonicalRepo is a repository's full name, as Docker resolves it: "orderly"
// and "docker.io/library/orderly" are one repository, and
// "mirror.example/example/orderly" is another one than "example/orderly".
func canonicalRepo(name string) string {
	parts := strings.Split(name, "/")
	if len(parts) == 1 || (!strings.ContainsAny(parts[0], ".:") && parts[0] != "localhost") {
		parts = append([]string{"docker.io"}, parts...)
	}
	if parts[0] == "index.docker.io" {
		parts[0] = "docker.io"
	}
	if parts[0] == "docker.io" && len(parts) == 2 {
		parts = []string{"docker.io", "library", parts[1]}
	}
	return strings.Join(parts, "/")
}

// ImageUpdateUndone is a deploy with a check period whose new container failed it.
// The new container was removed and the old one put back (see Restored).
type ImageUpdateUndone struct{ err error }

func (e *ImageUpdateUndone) Error() string { return e.err.Error() }
func (e *ImageUpdateUndone) Unwrap() error { return e.err }

// ImageUpdateResult is what a deploy with a check period did with the old image.
type ImageUpdateResult struct {
	OldImage string
	NewImage string
	// OldKept says why the old image is still on disk, "" when it was removed or
	// did not change.
	OldKept string
}

// settle watches the new container for seconds. It returns why it failed, or nil.
func settle(ctx context.Context, cli *client.Client, id, name string, svc catalog.Service, seconds int, since time.Time) error {
	deadline := settleNow().Add(time.Duration(seconds) * time.Second)
	for {
		// Passing needs a look that started once the period was over: one that
		// began earlier can miss a stop that came while Docker answered it.
		started := settleNow()
		// The log is judged once, on a look after the period: an unless line
		// written late in the period still cancels a failure line before it.
		final := !started.Before(deadline)
		look, cancel := context.WithTimeout(ctx, settleLookTimeout)
		problem, seen := settleProblem(look, cli, id, svc, since, final)
		cancel()
		if problem != "" {
			return errors.New(i18n.T("go.runtime.update.failed", "name", name, "problem", problem, "seconds", seconds))
		}
		if final && seen {
			return nil
		}
		now := settleNow()
		if !now.Before(deadline.Add(settleGrace)) {
			return errors.New(i18n.T("go.runtime.update.unreadable", "name", name, "seconds", seconds))
		}
		wait := settlePoll
		if now.Before(deadline) {
			wait = min(wait, deadline.Sub(now))
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: %w", name, ctx.Err())
		case <-time.After(wait):
		}
	}
}

// settleProblem says what is wrong with the new container now, or "". With
// logs, it also judges the catalog failure signals in the log of the period.
// seen is false when Docker did not describe it on this look, or did not return
// the log it was asked for.
func settleProblem(ctx context.Context, cli *client.Client, id string, svc catalog.Service, since time.Time, logs bool) (problem string, seen bool) {
	got, err := cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return i18n.T("go.runtime.update.gone"), true
	}
	if err != nil {
		// A daemon that does not answer one look says nothing about the container.
		return "", false
	}
	c := got.Container
	if c.State == nil || !c.State.Running || c.State.Restarting {
		status := ""
		if c.State != nil {
			status = string(c.State.Status)
		}
		return i18n.T("go.runtime.update.stopped", "status", status), true
	}
	if c.RestartCount > 0 {
		return i18n.T("go.runtime.update.restarted", "count", c.RestartCount), true
	}
	if c.State.Health != nil && c.State.Health.Status == container.Unhealthy {
		return i18n.T("go.runtime.update.unhealthy"), true
	}
	if !logs {
		return "", true
	}
	means, read := failureSignal(ctx, cli, id, svc, since)
	if !read {
		return "", false
	}
	if means != "" {
		return i18n.T("go.runtime.update.signal", "means", means), true
	}
	return "", true
}

// failureSignal is the meaning of the first catalog failure signal in the new
// container's log since since, or "". Idle signals and those judged over hours
// are left out: two minutes cannot show them. The whole log is read, line by
// line, never a first part or a tail, so a failure line is not lost among the
// lines around it, and an unless line anywhere in the period still counts.
// read is false when Docker did not return the log.
func failureSignal(ctx context.Context, cli *client.Client, id string, svc catalog.Service, since time.Time) (means string, read bool) {
	scan := &signalScan{}
	for _, s := range svc.Docker.HealthSignals {
		if s.State == "idle" || s.WindowHours != 0 || s.Pattern == "" {
			continue
		}
		re, err := regexp.Compile("(?i)" + s.Pattern)
		if err != nil {
			continue
		}
		var unless *regexp.Regexp
		if s.Unless != "" {
			unless, _ = regexp.Compile("(?i)" + s.Unless)
		}
		scan.signals = append(scan.signals, scannedSignal{means: s.Means, pattern: re, unless: unless})
	}
	if len(scan.signals) == 0 {
		return "", true
	}
	reader, err := cli.ContainerLogs(ctx, id, client.ContainerLogsOptions{
		ShowStdout: true, ShowStderr: true, Since: strconv.FormatInt(since.Unix(), 10),
	})
	if err != nil {
		return "", false
	}
	defer reader.Close()
	if err := demuxLog(reader, scan); err != nil || scan.overlong {
		return "", false
	}
	scan.flush()
	return scan.found(), true
}

type scannedSignal struct {
	means          string
	pattern        *regexp.Regexp
	unless         *regexp.Regexp
	hit, unlessHit bool
}

// signalScan judges a log line by line as it is written to it. overlong is a
// line past settleLineBytes, which it cannot judge.
type signalScan struct {
	signals  []scannedSignal
	line     []byte
	overlong bool
}

func (s *signalScan) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\n' {
			s.flush()
			continue
		}
		if len(s.line) >= settleLineBytes {
			s.overlong = true
			continue
		}
		s.line = append(s.line, b)
	}
	return len(p), nil
}

func (s *signalScan) flush() {
	if len(s.line) == 0 {
		return
	}
	for i := range s.signals {
		sig := &s.signals[i]
		sig.hit = sig.hit || sig.pattern.Match(s.line)
		sig.unlessHit = sig.unlessHit || (sig.unless != nil && sig.unless.Match(s.line))
	}
	s.line = s.line[:0]
}

func (s *signalScan) found() string {
	for _, sig := range s.signals {
		if sig.hit && !sig.unlessHit {
			return sig.means
		}
	}
	return ""
}

// demuxLog copies a container log to w without Docker's 8-byte frame headers.
// A log that is not framed (a container with a terminal) is copied as it is.
func demuxLog(r io.Reader, w io.Writer) error {
	br := bufio.NewReader(r)
	for {
		header, err := br.Peek(8)
		if len(header) == 0 && errors.Is(err, io.EOF) {
			return nil
		}
		if len(header) < 8 || header[0] > 2 || !bytes.Equal(header[1:4], []byte{0, 0, 0}) {
			_, err := io.Copy(w, br)
			return err
		}
		size := int64(binary.BigEndian.Uint32(header[4:8]))
		if _, err := br.Discard(8); err != nil {
			return err
		}
		if _, err := io.CopyN(w, br, size); err != nil {
			return err
		}
	}
}

// removeOldImage deletes the image the replaced container ran, never by force.
// It returns why the image stays, "" when it went or was already gone.
func removeOldImage(cli *client.Client, imageID string) string {
	ctx, cancel := context.WithTimeout(context.Background(), imageRemoveTimeout)
	defer cancel()
	_, err := cli.ImageRemove(ctx, imageID, client.ImageRemoveOptions{})
	switch {
	case err == nil, cerrdefs.IsNotFound(err):
		return ""
	case cerrdefs.IsConflict(err):
		return i18n.T("go.runtime.update.imageInUse")
	default:
		return err.Error()
	}
}
