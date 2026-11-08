package main

import (
	"context"
	"log"
	neturl "net/url"
	"slices"
	"strings"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/collectors"
)

// A registration the provider asked to retry is tried again this long after the
// first refusal, then twice as long each time up to the cap; after the give-up the
// user is told to add the device by hand. The same numbers as the web app.
const (
	deviceLinkFirstRetry = 6 * time.Minute
	deviceLinkMaxRetry   = 4 * time.Hour
	deviceLinkGiveUp     = 24 * time.Hour
	// deviceLinkTick is how often the retry pass looks for a registration that is due.
	deviceLinkTick = 3 * time.Minute
)

// deployedDevice says whether slug is deployed here with a device link, and the
// device id its saved credentials hold. An error means the store could not be read,
// which proves nothing either way.
func (a *App) deployedDevice(slug string) (id string, deployed bool, err error) {
	if a.store == nil || a.catalog == nil {
		return "", false, nil
	}
	svc, ok := a.catalog.Get(slug)
	if !ok || svc.DeviceLink == nil {
		return "", false, nil
	}
	if _, deployed, err = a.store.GetDeployment(slug); err != nil || !deployed {
		return "", false, err
	}
	creds, err := a.store.GetCredentials(slug)
	if err != nil {
		return "", false, err
	}
	return strings.TrimSpace(creds[svc.DeviceLink.IDEnv]), true, nil
}

// runsDevice is whether slug is deployed here with deviceID; false when unsure.
func (a *App) runsDevice(slug, deviceID string) bool {
	id, deployed, err := a.deployedDevice(slug)
	return err == nil && deployed && id == deviceID
}

// dropOtherDeviceLinks forgets slug's pending registrations of any device but id:
// a deploy replaced that device.
func (a *App) dropOtherDeviceLinks(slug, id string) {
	if a.store == nil {
		return
	}
	rows, err := a.store.ListPendingDeviceLinks()
	if err != nil {
		return
	}
	for _, row := range rows {
		if row.Slug == slug && row.DeviceID != id {
			_ = a.store.DeletePendingDeviceLink(row.Slug, row.DeviceID)
		}
	}
}

// pendingDeviceLinkIDs is, per slug, every device id with a stored registration,
// retrying or given up.
func (a *App) pendingDeviceLinkIDs() map[string]map[string]bool {
	out := map[string]map[string]bool{}
	if a.store == nil {
		return out
	}
	rows, err := a.store.ListPendingDeviceLinks()
	if err != nil {
		return out
	}
	for _, row := range rows {
		if out[row.Slug] == nil {
			out[row.Slug] = map[string]bool{}
		}
		out[row.Slug][row.DeviceID] = true
	}
	return out
}

// retryDeviceLinks retries the registrations a provider asked to try again later,
// with backoff, and gives up after a day. A service no longer deployed here is
// forgotten without asking the provider. A row whose id the saved credentials no
// longer hold is left alone: credentials saved without a redeploy leave the old
// device running, and a deploy drops the row itself (dropOtherDeviceLinks).
func (a *App) retryDeviceLinks(ctx context.Context, now time.Time) {
	devicesReg, ok := a.collectors.(deviceRegistry)
	if !ok || a.store == nil {
		return
	}
	rows, err := a.store.ListPendingDeviceLinks()
	if err != nil {
		log.Printf("device links: list: %v", err)
		return
	}
	for _, row := range rows {
		if ctx.Err() != nil {
			return
		}
		id, deployed, err := a.deployedDevice(row.Slug)
		if err != nil {
			continue
		}
		if !deployed {
			_ = a.store.DeletePendingDeviceLink(row.Slug, row.DeviceID)
			continue
		}
		if id != row.DeviceID || row.GaveUp || row.NextAt.After(now) {
			continue
		}
		creds, err := a.store.GetCredentials(row.Slug)
		if err != nil {
			continue
		}
		callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		result := devicesReg.RegisterDevice(callCtx, row.Slug, creds)
		cancel()
		if result == collectors.RegisterAdded {
			_ = a.store.DeletePendingDeviceLink(row.Slug, row.DeviceID)
			a.store.RecordEvent(row.Slug, "device_linked", row.DeviceID+" added to the account")
			// A redeploy during the call may have put another device in its place.
			if a.runsDevice(row.Slug, row.DeviceID) {
				a.setAddedDevice(row.Slug, row.DeviceID)
			}
			continue
		}
		attempts := row.Attempts + 1
		if now.Sub(row.FirstAt) >= deviceLinkGiveUp {
			_ = a.store.UpdatePendingDeviceLink(row.Slug, row.DeviceID, attempts, row.NextAt, true)
			continue
		}
		wait := deviceLinkMaxRetry
		if attempts < 16 { // past that the doubling is far over the cap anyway
			wait = min(deviceLinkFirstRetry<<attempts, deviceLinkMaxRetry)
		}
		_ = a.store.UpdatePendingDeviceLink(row.Slug, row.DeviceID, attempts, now.Add(wait), false)
	}
}

// startDeviceLinkRetry runs retryDeviceLinks every few minutes until ctx ends.
func (a *App) startDeviceLinkRetry(ctx context.Context) {
	if ctx == nil || a.store == nil {
		return
	}
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	a.deviceLinkStop = func() {
		cancel()
		<-done
	}
	go func() {
		defer close(done)
		ticker := time.NewTicker(deviceLinkTick)
		defer ticker.Stop()
		for {
			a.retryDeviceLinks(loopCtx, time.Now())
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// stopDeviceLinkRetry stops the loop and waits for it, so Shutdown never closes the
// store under a retry being written.
func (a *App) stopDeviceLinkRetry() {
	if a.deviceLinkStop != nil {
		a.deviceLinkStop()
		a.deviceLinkStop = nil
	}
}

// retryingDeviceIDs is, per slug, the device id the app is still trying to add to
// the account, so the deploy output can say so instead of the manual step.
func (a *App) retryingDeviceIDs() map[string]string {
	out := map[string]string{}
	if a.store == nil {
		return out
	}
	rows, err := a.store.ListPendingDeviceLinks()
	if err != nil {
		return out
	}
	for _, row := range rows {
		if !row.GaveUp && a.runsDevice(row.Slug, row.DeviceID) {
			out[row.Slug] = row.DeviceID
		}
	}
	return out
}

// deviceLinkGaveUpNotifications tells the user about every device the provider
// never accepted after a day of retries, that still runs here, and that the
// provider's last check still says is not on the account: a device the user then
// adds by hand stops being reported at the next check. Read from the store, so a
// restart cannot drop one.
func (a *App) deviceLinkGaveUpNotifications() []Notification {
	devicesReg, ok := a.collectors.(deviceRegistry)
	if !ok || a.store == nil || a.catalog == nil {
		return nil
	}
	rows, err := a.store.ListPendingDeviceLinks()
	if err != nil {
		return nil
	}
	var items []Notification
	for _, row := range rows {
		svc, ok := a.catalog.Get(row.Slug)
		if !row.GaveUp || !ok || svc.DeviceLink == nil || !a.runsDevice(row.Slug, row.DeviceID) ||
			!slices.Contains(devicesReg.Unlinked(row.Slug), row.DeviceID) {
			continue
		}
		url := strings.ReplaceAll(svc.DeviceLink.URL, "${"+svc.DeviceLink.IDEnv+"}", neturl.PathEscape(row.DeviceID))
		items = append(items, Notification{
			Level: "warning",
			Title: svc.Name + " device not on your account",
			Message: strings.TrimSpace(svc.Name + " never accepted the device " + row.DeviceID +
				" after a day of retries, so it earns for nobody. " + strings.TrimSpace(svc.DeviceLink.Steps) + " " + url),
		})
	}
	return items
}
