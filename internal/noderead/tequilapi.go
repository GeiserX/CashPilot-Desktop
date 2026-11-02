package noderead

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"
)

// MysteriumTequilAPI is the native Mysterium node's TequilAPI. The catalog's native
// command binds it to 127.0.0.1 and leaves the port at the node's default, 4050.
const MysteriumTequilAPI = "http://127.0.0.1:4050"

// tequilAPITimeout bounds each request, as the catalog command's wget -T 5 does.
const tequilAPITimeout = 5 * time.Second

// maxTequilAPIBody caps one answer; a node's identity and settlement history are a
// few kilobytes.
const maxTequilAPIBody = 1 << 20

// localClient talks to a loopback address only, so no proxy from the environment
// may sit in between.
var localClient = &http.Client{Transport: &http.Transport{Proxy: nil}}

var identityID = regexp.MustCompile(`0x[0-9a-fA-F]{40}`)

// TequilAPIOutput does in Go what Mysterium's node_read command does in its
// container, and prints the same text: the first identity from /identities, that
// identity's answer, then a newline and the settlement history when it can be read.
// A failed history read leaves the figures, as the command's `|| true` does.
func TequilAPIOutput(ctx context.Context, client *http.Client, base string) (string, error) {
	ids, err := tequilAPIGet(ctx, client, base+"/identities")
	if err != nil {
		return "", err
	}
	id := identityID.FindString(ids)
	if id == "" {
		return "", fmt.Errorf("the node has no identity yet")
	}
	out, err := tequilAPIGet(ctx, client, base+"/identities/"+id)
	if err != nil {
		return "", err
	}
	if history, err := tequilAPIGet(ctx, client, base+"/transactor/settle/history?page_size=1000"); err == nil {
		out += "\n" + history
	}
	return out, nil
}

func tequilAPIGet(ctx context.Context, client *http.Client, url string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, tequilAPITimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("TequilAPI answered %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTequilAPIBody+1))
	if err != nil {
		return "", err
	}
	if len(body) > maxTequilAPIBody {
		return "", fmt.Errorf("TequilAPI answer over %d bytes", maxTequilAPIBody)
	}
	return string(body), nil
}
