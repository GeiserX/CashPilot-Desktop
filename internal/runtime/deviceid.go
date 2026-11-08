package runtime

import (
	"crypto/sha3"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// deviceIDShape is the only id format a copy can derive its own from: hex in one case.
var deviceIDShape = regexp.MustCompile(`^(?:[0-9a-f]+|[0-9A-F]+)$`)

// copyDeviceID is the device id of a copy: same format, its own value. The provider
// knows each device by this id (ProxyRack's 64-hex UUID), so a copy that kept the
// original's would report as the same device and earn nothing of its own. Derived,
// not random, so a redeploy keeps the copy's device. seed is "p<proxy id>" for a
// proxied copy and "<n>" for copy n on the box.
//
// It is the web app's copy_device_id (app/proxies.py) byte for byte: SHAKE-256 of
// "<id>-<seed>", cut to the original's length, upper case only when the original is.
// Any other format, or a result the catalog's pattern refuses, is an error: the copy
// is refused rather than given a cloned identity.
func copyDeviceID(deviceID, seed, pattern string) (string, error) {
	if !deviceIDShape.MatchString(deviceID) {
		return "", errors.New("its device id is not in a format a copy can derive its own from")
	}
	sum := sha3.SumSHAKE256([]byte(deviceID+"-"+seed), (len(deviceID)+1)/2)
	derived := hex.EncodeToString(sum)[:len(deviceID)]
	// Python's str.isupper: at least one letter, and no lower-case one.
	if strings.ToUpper(deviceID) == deviceID && strings.ToLower(deviceID) != deviceID {
		derived = strings.ToUpper(derived)
	}
	if pattern != "" {
		re, err := regexp.Compile(`^(?:` + pattern + `)$`)
		if err != nil || !re.MatchString(derived) {
			return "", errors.New("the device id derived for the copy does not match the catalog's format for it")
		}
	}
	return derived, nil
}

// renameDeviceIDs gives a copy its own value for every env key the catalog marks
// per_device, and puts it wherever the old one appears in the env (a device name
// filled from it included; the command is built from the env afterwards). A required
// per_device value that is empty, or one that cannot be derived, refuses the copy.
func renameDeviceIDs(svc catalog.Service, env map[string]string, seed string) error {
	var renamed [][2]string
	for _, item := range svc.Docker.Env {
		if !item.PerDevice {
			continue
		}
		old := env[item.Key]
		if old == "" {
			if item.Required {
				// Empty is only safe where the service makes its own id when given none.
				return fmt.Errorf("its deployment has no %s, so the copy cannot be given one of its own", item.Key)
			}
			continue
		}
		derived, err := copyDeviceID(old, seed, item.Pattern)
		if err != nil {
			return err
		}
		renamed = append(renamed, [2]string{old, derived})
	}
	for key, value := range env {
		for _, r := range renamed {
			value = strings.ReplaceAll(value, r[0], r[1])
		}
		env[key] = value
	}
	return nil
}
