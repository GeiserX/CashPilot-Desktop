package activity

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func table(rows int) string {
	var b strings.Builder
	b.WriteString("1.0.0.0\t1.0.0.255\t13335\tUS\tCLOUDFLARENET\n")
	b.WriteString("2.0.0.0\t2.0.0.255\t0\tNone\tNot routed\n")
	b.WriteString("2001:db8::\t2001:db8::ffff\t64500\tUnknown\tDOC\n")
	for i := 0; i < rows; i++ {
		fmt.Fprintf(&b, "10.%d.%d.0\t10.%d.%d.255\t%d\tDE\tNET%d\n", i/256, i%256, i/256, i%256, 65000+i%100, i)
	}
	return b.String()
}

func TestIPGeoBuildAndLookupAtEdges(t *testing.T) {
	g := &IPGeo{Dir: t.TempDir(), URL: "x"}
	n, err := g.Build(strings.NewReader(table(1200)))
	if err != nil || n != 1202 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	got := g.Lookup([]string{"1.0.0.0", "1.0.0.255", "1.0.1.0", "2.0.0.5", "2001:db8::1", "::ffff:1.0.0.7"})
	if got["1.0.0.0"].ASN != "AS13335 CLOUDFLARENET" || got["1.0.0.0"].CC != "US" || got["1.0.0.255"].CC != "US" || got["::ffff:1.0.0.7"].CC != "US" {
		t.Fatalf("got %v", got)
	}
	if _, ok := got["1.0.1.0"]; ok {
		t.Fatal("past the end of a range is unknown")
	}
	if _, ok := got["2.0.0.5"]; ok {
		t.Fatal("AS 0 is not routed and names nothing")
	}
	if got["2001:db8::1"].CC != "" || got["2001:db8::1"].ASN != "AS64500 DOC" {
		t.Fatalf("an unknown country reads empty: %v", got["2001:db8::1"])
	}
	st := g.Status()
	if !st.Present || st.Rows == nil || *st.Rows != 1202 {
		t.Fatalf("status %+v", st)
	}
}

func TestIPGeoRefusesBadTables(t *testing.T) {
	g := &IPGeo{Dir: t.TempDir(), URL: "x"}
	if _, err := g.Build(strings.NewReader(table(10))); err == nil {
		t.Fatal("a tiny table must not replace the real one")
	}
	if _, err := g.Build(strings.NewReader(strings.Repeat("x", ipgeoMaxLine+10) + "\n")); err == nil {
		t.Fatal("an over-long line must be refused")
	}
	if _, err := g.Build(strings.NewReader(strings.Repeat("junk\n", ipgeoMaxBad+1))); err == nil {
		t.Fatal("too many bad lines must be refused")
	}
	if _, err := os.Stat(filepath.Join(g.Dir, ipgeoFileName)); err == nil {
		t.Fatal("a refused table must leave no file behind")
	}
	if len(g.Lookup([]string{"1.0.0.1"})) != 0 {
		t.Fatal("no table means unknown")
	}
}

func TestIPGeoDownload(t *testing.T) {
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	_, _ = w.Write([]byte(table(1200)))
	_ = w.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { _, _ = rw.Write(gz.Bytes()) }))
	defer srv.Close()
	g := &IPGeo{Dir: t.TempDir(), URL: srv.URL, Client: srv.Client()}
	if n, err := g.Download(context.Background()); err != nil || n != 1202 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if g.NeedsRefresh() {
		t.Fatal("a fresh table needs no refresh")
	}
	if (&IPGeo{Dir: t.TempDir()}).NeedsRefresh() {
		t.Fatal("an empty URL disables the download")
	}
}
