package config

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/filelock"
	"github.com/zalando/go-keyring"
)

const workerKeyFile = ".upstream_worker_key"
const workerKeyVersion = "v1:"

// WorkerCredential is the key issued at Join and the pairing it belongs to.
type WorkerCredential struct {
	Key        string `json:"key"`
	Generation uint64 `json:"generation"`
	ServerURL  string `json:"server,omitempty"`
}

func (r WorkerCredential) encode() string {
	raw, _ := json.Marshal(r) // primitive fields only
	return workerKeyVersion + string(raw)
}

func decodeWorkerKey(value string) (WorkerCredential, error) {
	value, versioned := strings.CutPrefix(strings.TrimSpace(value), workerKeyVersion)
	if !versioned {
		return WorkerCredential{Key: value}, nil
	}
	var record WorkerCredential
	if err := json.Unmarshal([]byte(value), &record); err != nil || record.Generation == 0 {
		return WorkerCredential{}, errors.New("invalid saved worker credential")
	}
	return record, nil
}

// UpstreamWorkerKey returns the per-worker key for command authentication.
// It also promotes a versioned fallback once the credential store recovers.
func UpstreamWorkerKey(appDir string) (string, error) {
	record, err := readWorkerCredential(appDir, true)
	return record.Key, err
}

// ReadUpstreamWorkerCredential reads the pairing's credential without changing
// either store. Heartbeats use its generation to discard an earlier reply.
func ReadUpstreamWorkerCredential(appDir string) (WorkerCredential, error) {
	return readWorkerCredential(appDir, false)
}

func readWorkerCredential(appDir string, promote bool) (WorkerCredential, error) {
	path := filepath.Join(appDir, workerKeyFile)
	release, err := lockWorkerKey(path)
	if err != nil {
		return WorkerCredential{}, err
	}
	defer release()

	generation, err := workerKeyGeneration(path)
	if err != nil {
		return WorkerCredential{}, err
	}
	raw, storeErr := keyring.Get(keyringService, keyringUserUpstreamWorker)
	stored, err := decodeWorkerKey(raw)
	if err != nil {
		return WorkerCredential{}, err
	}
	rawFile, fileErr := os.ReadFile(path)
	if fileErr != nil && !errors.Is(fileErr, os.ErrNotExist) {
		return WorkerCredential{}, fileErr
	}
	fallback, err := decodeWorkerKey(string(rawFile))
	if err != nil {
		return WorkerCredential{}, err
	}

	if fileErr == nil && fallback.Generation >= generation &&
		(fallback.Generation > stored.Generation || storeErr != nil ||
			(fallback.Generation == 0 && stored.Generation == 0 && stored.Key == "")) {
		if promote && (storeErr == nil || errors.Is(storeErr, keyring.ErrNotFound)) {
			if fallback.Generation == 0 {
				// Legacy fallback files stay usable on a headless restart.
				_ = keyring.Set(keyringService, keyringUserUpstreamWorker, fallback.Key)
			} else {
				_ = promoteWorkerKey(path, fallback)
			}
		}
		return fallback, nil
	}
	if stored.Generation < generation || refuseKeyRegen(storeErr, runtimeGOOS) {
		return WorkerCredential{}, errors.New("the current worker key is unavailable (credential store locked or the save did not finish)")
	}
	if promote && fileErr == nil && stored.Generation > 0 {
		dropFallback(path)
	}
	return stored, nil
}

// SetUpstreamWorkerKey revokes the previous key on Leave or repointing. The
// empty key gets a generation too, so a locked store cannot undo a Leave.
func SetUpstreamWorkerKey(appDir, value string) error {
	return SetJoinedWorkerKey(appDir, "", value)
}

// SetJoinedWorkerKey records the new pairing's key before publishing its mode
// and server address. The server address also guards sends from the old loop.
func SetJoinedWorkerKey(appDir, serverURL, value string) error {
	path := filepath.Join(appDir, workerKeyFile)
	release, err := lockWorkerKey(path)
	if err != nil {
		return err
	}
	defer release()

	generation, err := workerKeyGeneration(path)
	if err != nil {
		return err
	}
	next := WorkerCredential{Key: strings.TrimSpace(value), Generation: generation + 1, ServerURL: serverURL}
	// Reserve the generation first. If the process stops before saving the
	// new key, readers fail closed instead of accepting the previous key.
	if err := writeFileAtomic(path+".generation", []byte(strconv.FormatUint(next.Generation, 10))); err != nil {
		return err
	}
	if err := promoteWorkerKey(path, next); err != nil {
		return writeFileAtomic(path, []byte(next.encode()))
	}
	return nil
}

// UpstreamWorkerKeyGeneration reads the latest recorded change even if the
// credential store locked while a heartbeat was in flight.
func UpstreamWorkerKeyGeneration(appDir string) (uint64, error) {
	return workerKeyGeneration(filepath.Join(appDir, workerKeyFile))
}

func lockWorkerKey(path string) (func(), error) {
	release, ok := filelock.Acquire(context.Background(), path+".lock", 5*time.Second)
	if !ok {
		return nil, errors.New("could not lock the worker credential")
	}
	return release, nil
}

func workerKeyGeneration(path string) (uint64, error) {
	raw, err := os.ReadFile(path + ".generation")
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(string(raw), 10, 64)
}

func promoteWorkerKey(path string, record WorkerCredential) error {
	if err := keyring.Set(keyringService, keyringUserUpstreamWorker, record.encode()); err != nil {
		return err
	}
	dropFallback(path)
	return nil
}
