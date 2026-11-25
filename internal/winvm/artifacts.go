package winvm

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

const artifactLogName = ".cashpilot-files.jsonl"

var artifactMu sync.Mutex

type artifactRecord struct {
	Root string `json:"root"`
	Path string `json:"path"`
	ID   string `json:"id,omitempty"`
}

type artifactLog struct {
	dir  string
	root string
	id   string
	file *os.File
}

func openArtifactLog(dir string, create bool) (*artifactLog, error) {
	root, err := artifactID(dir)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, artifactLogName)
	var f *os.File
	if create {
		f, err = os.OpenFile(path, os.O_RDWR|os.O_APPEND|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			j := &artifactLog{dir: dir, root: root, file: f}
			j.id, err = artifactID(path)
			if err == nil {
				err = j.record(artifactLogName, j.id)
			}
			if err != nil {
				f.Close()
				return nil, err
			}
			return j, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("the file journal is not a regular file")
	}
	f, err = os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0)
	if err != nil {
		return nil, err
	}
	j := &artifactLog{dir: dir, root: root, file: f}
	j.id, err = artifactID(path)
	if err == nil {
		_, err = j.records()
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return j, nil
}

func (j *artifactLog) record(rel, id string) error {
	b, err := json.Marshal(artifactRecord{Root: j.root, Path: rel, ID: id})
	if err != nil {
		return err
	}
	if _, err = j.file.Write(append(b, '\n')); err != nil {
		return err
	}
	return j.file.Sync()
}

func (j *artifactLog) records() (map[string][]string, error) {
	if _, err := j.file.Seek(0, 0); err != nil {
		return nil, err
	}
	r := bufio.NewScanner(j.file)
	out := make(map[string][]string)
	first := true
	for r.Scan() {
		var entry artifactRecord
		if err := json.Unmarshal(r.Bytes(), &entry); err != nil {
			return nil, fmt.Errorf("the file journal contains an unreadable record: %w", err)
		}
		if first && (entry.Path != artifactLogName || entry.ID != j.id) {
			return nil, errors.New("the file journal identity does not match")
		}
		first = false
		rel := filepath.FromSlash(entry.Path)
		if entry.Root != j.root || !filepath.IsLocal(rel) || filepath.Clean(rel) != rel || rel == "." {
			return nil, errors.New("the file journal contains an unverified path")
		}
		if entry.ID != "" {
			out[rel] = append(out[rel], entry.ID)
		}
	}
	if first {
		return nil, errors.New("the file journal has no identity")
	}
	return out, r.Err()
}

func randomArtifactName(prefix, suffix string) string {
	var nonce [16]byte
	rand.Read(nonce[:])
	return prefix + hex.EncodeToString(nonce[:]) + suffix
}

var errUntrackedFile = errors.New("file ID absent from the journal")

func untrackedFile(path string) error {
	return fmt.Errorf("%s: %w", path, errUntrackedFile)
}

func (j *artifactLog) verify(rel string) error {
	id, err := artifactID(filepath.Join(j.dir, rel))
	if err != nil {
		return err
	}
	records, err := j.records()
	if err != nil {
		return err
	}
	if !slices.Contains(records[rel], id) {
		return untrackedFile(filepath.Join(j.dir, rel))
	}
	return nil
}

func (j *artifactLog) note(rel string) (string, error) {
	id, err := artifactID(filepath.Join(j.dir, rel))
	if err != nil {
		return "", err
	}
	return id, j.record(rel, id)
}

// removeRecorded checks the live parent identity before deleting the file
// through the handle that supplied its file ID.
func removeRecorded(dir, rootID, rel string, ids []string) error {
	rootNow, err := artifactID(dir)
	if err != nil {
		return err
	}
	if rootNow != rootID {
		return untrackedFile(dir)
	}
	path := filepath.Join(dir, rel)
	for parent := filepath.Dir(path); parent != dir; parent = filepath.Dir(parent) {
		fi, err := os.Lstat(parent)
		if err != nil {
			return err
		}
		if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
			return untrackedFile(parent)
		}
	}
	return deleteArtifact(path, ids)
}

func removeJournaledFile(path string) error {
	if _, err := os.Lstat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	dir, rel := filepath.Dir(path), filepath.Base(path)
	j, err := openArtifactLog(dir, false)
	if err != nil {
		return err
	}
	defer j.file.Close()
	records, err := j.records()
	if err != nil {
		return err
	}
	return removeRecorded(dir, j.root, rel, records[rel])
}

func cleanupArtifacts(dir string, captureLive bool, report func(string, error)) {
	j, err := openArtifactLog(dir, false)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			report(artifactLogName, err)
		}
		return
	}
	records, err := j.records()
	j.file.Close()
	if err != nil {
		report(artifactLogName, err)
		return
	}
	exe, _ := os.Executable()
	var paths []string
	for name := range records {
		if name != PlanFile && name != artifactLogName {
			paths = append(paths, name)
		}
	}
	// Descendants sort after their directory.
	slices.Sort(paths)
	slices.Reverse(paths)
	for _, rel := range paths {
		if strings.EqualFold(filepath.Join(dir, rel), exe) ||
			(captureLive && (rel == proofDir || strings.HasPrefix(rel, proofDir+string(filepath.Separator)))) {
			continue
		}
		path := filepath.Join(dir, rel)
		if info, err := os.Lstat(path); err == nil && info.IsDir() {
			if entries, err := os.ReadDir(path); err == nil && len(entries) != 0 {
				continue // Keep directories that still contain independent files.
			}
		}
		err := removeRecorded(dir, j.root, rel, records[rel])
		if !errors.Is(err, fs.ErrNotExist) {
			report(rel, err)
		}
	}
}

func finishArtifactRemoval(dir string) (bool, error) {
	j, err := openArtifactLog(dir, false)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil // A legacy plan has no file identity.
	}
	if err != nil {
		return false, err
	}
	records, err := j.records()
	j.file.Close()
	if err != nil {
		return false, err
	}
	if err = removeRecorded(dir, j.root, PlanFile, records[PlanFile]); err != nil {
		return false, err
	}
	if err = removeRecorded(dir, j.root, artifactLogName, []string{j.id}); err != nil {
		return true, err
	}
	return true, nil
}

// CheckJournaledFile verifies a file the elevated bootstrap created.
func CheckJournaledFile(dir, path string) error {
	rel, err := filepath.Rel(dir, path)
	if err != nil || !filepath.IsLocal(rel) || rel == "." {
		return untrackedFile(path)
	}
	j, err := openArtifactLog(dir, false)
	if err != nil {
		return err
	}
	defer j.file.Close()
	return j.verify(rel)
}

func (j *artifactLog) makeDir(rel string) error {
	if err := j.record(rel, ""); err != nil {
		return err
	}
	if err := os.Mkdir(filepath.Join(j.dir, rel), 0o700); err != nil {
		return err
	}
	_, err := j.note(rel)
	return err
}

func (j *artifactLog) remove(rel string) error {
	records, err := j.records()
	if err != nil {
		return err
	}
	return removeRecorded(j.dir, j.root, rel, records[rel])
}

// RotateJournaledLog moves a log only when both paths belong to the journal.
func RotateJournaledLog(path string) error {
	artifactMu.Lock()
	defer artifactMu.Unlock()
	dir, rel := filepath.Dir(path), filepath.Base(path)
	j, err := openArtifactLog(dir, false)
	if err != nil {
		return err
	}
	defer j.file.Close()
	id, err := artifactID(path)
	if err != nil {
		return err
	}
	if err := j.verify(rel); err != nil {
		return err
	}
	if err := j.verify(rel + ".1"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := j.record(rel+".1", id); err != nil {
		return err
	}
	return os.Rename(path, path+".1")
}

// CheckCreatedDirectory verifies the directory record written when the
// elevated bootstrap provisioned this instance.
func CheckCreatedDirectory(path string) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(resolved, path) {
		return untrackedFile(path)
	}
	if err := CheckSetupAccess(path); err != nil {
		return err
	}
	id, err := artifactID(path)
	if err != nil {
		return err
	}
	b, err := readCapped(filepath.Join(path, ".cashpilot-directory"), 200, true)
	if err != nil {
		return err
	}
	if string(b) != "cashpilot-data:"+id {
		return untrackedFile(path)
	}
	return nil
}
