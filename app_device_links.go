package main

import (
	"context"
	"log"
	neturl "net/url"
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

// deployedDeviceID is the device id the deployment of slug runs, or "" when slug
// is not deployed here or has no device link.
func (a *App) deployedDeviceID(slug string) string {
	if a.store == nil || a.catalog == nil {
		return ""
	}
	svc, ok := a.catalog.Get(slug)
	if !ok || svc.DeviceLink == nil {
		return ""
	}
	if _, deployed, err := a.store.GetDeployment(slug); err != nil || !deployed {
		return ""
	}
	creds, err := a.store.GetCredentials(slug)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(creds[svc.DeviceLink.IDEnv])
}

// retryDeviceLinks retries the registrations a provider asked to try again later,
// with backoff, and gives up after a day. A device that no longer runs here is
// forgotten without asking the provider.
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
		if a.deployedDeviceID(row.Slug) != row.DeviceID {
			_ = a.store.DeletePendingDeviceLink(row.Slug, row.DeviceID)
			continue
		}
		if row.GaveUp || row.NextAt.After(now) {
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
			a.setAddedDevice(row.Slug, row.DeviceID)
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
		if !row.GaveUp {
			out[row.Slug] = row.DeviceID
		}
	}
	return out
}

// deviceLinkGaveUpNotifications tells the user about every device the provider
// never accepted after a day of retries and that still runs here. Read from the
// store, so a restart cannot drop one.
func (a *App) deviceLinkGaveUpNotifications() []Notification {
	if a.store == nil || a.catalog == nil {
		return nil
	}
	rows, err := a.store.ListPendingDeviceLinks()
	if err != nil {
		return nil
	}
	var items []Notification
	for _, row := range rows {
		svc, ok := a.catalog.Get(row.Slug)
		if !row.GaveUp || !ok || svc.DeviceLink == nil || a.deployedDeviceID(row.Slug) != row.DeviceID {
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
