package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const workerKeyFile = ".upstream_worker_key"

// LegacyWorkerKey reads the pre-revision credential for the window's upgrade
// enrolment. Only a live server reply can publish it for command authorization.
func LegacyWorkerKey(appDir, serverURL string) (string, error) {
	path := filepath.Join(appDir, workerKeyFile)
	marker, err := os.ReadFile(path + ".generation")
	var generation uint64
	if err == nil {
		generation, err = strconv.ParseUint(string(marker), 10, 64)
		if err != nil {
			return "", err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	raw, storeErr := credentials.Get(keyringService, keyringUserUpstreamWorker)
	fallback, fileErr := os.ReadFile(path)
	// The old writer used a fallback when the keychain kept an earlier key.
	for _, value := range []string{string(fallback), raw} {
		value = strings.TrimSpace(value)
		if data, ok := strings.CutPrefix(value, "v1:"); ok {
			var key struct {
				Key        string `json:"key"`
				Generation uint64 `json:"generation"`
				Server     string `json:"server"`
			}
			if json.Unmarshal([]byte(data), &key) == nil && generation != 0 &&
				key.Generation == generation && (key.Server == "" || key.Server == serverURL) {
				return key.Key, nil
			}
		} else if generation == 0 && value != "" {
			return value, nil
		}
	}
	if generation != 0 || refuseKeyRegen(storeErr, runtimeGOOS) || (fileErr != nil && !errors.Is(fileErr, os.ErrNotExist)) {
		return "", errors.New("the saved worker key is unavailable")
	}
	return "", nil
}
