package activity

import (
	"bufio"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// The network table: iptoasn.com's ip2asn-combined, "Licensed under Public
// Domain (PDDL v1.0)" in its own words, looked up locally. No address is ever
// sent anywhere. The country is where the network is registered, not where the
// address is. A port of app/ipgeo.py.
const (
	IPGeoURL        = "https://iptoasn.com/data/ip2asn-combined.tsv.gz"
	IPGeoMaxAge     = 7 * 24 * time.Hour
	ipgeoTimeout    = 120 * time.Second
	ipgeoMinRows    = 1000
	ipgeoMaxRows    = 3_000_000
	ipgeoMaxDown    = 64 << 20
	ipgeoMaxTable   = 512 << 20
	ipgeoMaxLine    = 4096
	ipgeoMaxBad     = 1000
	ipgeoFileName   = "ipgeo.sqlite"
	ipgeoBatchCount = 5000
)

// Geo is a network label and the country it is registered in.
type Geo struct {
	ASN string `json:"asn"`
	CC  string `json:"cc"`
}

// IPGeo is the local table under the app's data directory.
type IPGeo struct {
	Dir    string
	URL    string
	Client *http.Client
	mu     sync.Mutex
}

// ErrDownloadBusy is a download asked for while another runs.
var ErrDownloadBusy = errors.New("a download is already running")

func (g *IPGeo) path() string { return filepath.Join(g.Dir, ipgeoFileName) }

// Key is the 32-hex key of an address, IPv4 mapped into IPv6, or "".
func Key(ip string) string {
	a, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return ""
	}
	if a.Is4() {
		a = netip.AddrFrom16(a.As16())
	}
	b := a.As16()
	return hex.EncodeToString(b[:])
}

// IPGeoStatus is what the page shows about the table.
type IPGeoStatus struct {
	Present    bool   `json:"present"`
	AgeSeconds *int64 `json:"ageSeconds"`
	Rows       *int64 `json:"rows"`
}

// Status says whether the table is there, how old it is and how many ranges it holds.
func (g *IPGeo) Status() IPGeoStatus {
	st, err := os.Stat(g.path())
	if err != nil {
		return IPGeoStatus{}
	}
	db, err := sql.Open("sqlite", "file:"+g.path()+"?mode=ro")
	if err != nil {
		return IPGeoStatus{}
	}
	defer db.Close()
	var rows string
	if err := db.QueryRow("SELECT v FROM meta WHERE k = 'rows'").Scan(&rows); err != nil {
		return IPGeoStatus{}
	}
	age := int64(time.Since(st.ModTime()).Seconds())
	n, _ := strconv.ParseInt(rows, 10, 64)
	return IPGeoStatus{Present: true, AgeSeconds: &age, Rows: &n}
}

// NeedsRefresh is true when the table is missing or older than IPGeoMaxAge.
func (g *IPGeo) NeedsRefresh() bool {
	if g.URL == "" {
		return false
	}
	st, err := os.Stat(g.path())
	return err != nil || time.Since(st.ModTime()) > IPGeoMaxAge
}

// Lookup is the network and country of each address the table knows. A missing
// or unreadable table gives an empty map: unknown, never a wrong answer.
func (g *IPGeo) Lookup(ips []string) map[string]Geo {
	found := map[string]Geo{}
	if len(ips) == 0 {
		return found
	}
	if _, err := os.Stat(g.path()); err != nil {
		return found
	}
	db, err := sql.Open("sqlite", "file:"+g.path()+"?mode=ro")
	if err != nil {
		return found
	}
	defer db.Close()
	for _, ip := range ips {
		if _, done := found[ip]; done {
			continue
		}
		k := Key(ip)
		if k == "" {
			continue
		}
		var end, cc, name string
		var asn int64
		err := db.QueryRow("SELECT end, asn, cc, name FROM ranges WHERE start <= ? ORDER BY start DESC LIMIT 1", k).Scan(&end, &asn, &cc, &name)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return map[string]Geo{}
		}
		if k <= end {
			label := strings.TrimSpace(fmt.Sprintf("AS%d %s", asn, name))
			if len(label) > 80 {
				label = label[:80]
			}
			found[ip] = Geo{ASN: label, CC: cc}
		}
	}
	return found
}

// Download fetches and builds the table. One at a time.
func (g *IPGeo) Download(ctx context.Context) (int, error) {
	if g.URL == "" {
		return 0, errors.New("disabled")
	}
	if !g.mu.TryLock() {
		return 0, ErrDownloadBusy
	}
	defer g.mu.Unlock()
	if err := os.MkdirAll(g.Dir, 0o700); err != nil {
		return 0, err
	}
	client := g.Client
	if client == nil {
		client = &http.Client{Timeout: ipgeoTimeout, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Redirects are followed, but never from https down to plain http.
			if req.URL.Scheme != "https" && strings.HasPrefix(strings.ToLower(g.URL), "https://") {
				return errors.New("the download was redirected off https")
			}
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return nil
		}}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.URL, nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}
	gzPath := filepath.Join(g.Dir, "ipgeo.tsv.gz.tmp")
	defer os.Remove(gzPath)
	f, err := os.OpenFile(gzPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, ipgeoMaxDown+1))
	f.Close()
	if err != nil {
		return 0, err
	}
	if n > ipgeoMaxDown {
		return 0, errors.New("the download is larger than the real table")
	}
	in, err := os.Open(gzPath)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	zr, err := gzip.NewReader(in)
	if err != nil {
		return 0, err
	}
	defer zr.Close()
	return g.Build(io.LimitReader(zr, ipgeoMaxTable+1))
}

type geoRow struct {
	start, end, cc, name string
	asn                  int64
}

// Build writes the table from TSV to a temp file, then moves it into place. A
// file too small, too large or too broken to be the real table leaves the
// existing one untouched.
func (g *IPGeo) Build(r io.Reader) (int, error) {
	if err := os.MkdirAll(g.Dir, 0o700); err != nil {
		return 0, err
	}
	tmp := g.path() + ".tmp"
	_ = os.Remove(tmp)
	db, err := sql.Open("sqlite", tmp)
	if err != nil {
		return 0, err
	}
	n, err := buildInto(db, r)
	db.Close()
	if err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	return n, os.Rename(tmp, g.path())
}

func buildInto(db *sql.DB, r io.Reader) (int, error) {
	if _, err := db.Exec(`CREATE TABLE ranges (start TEXT PRIMARY KEY, end TEXT NOT NULL, asn INTEGER, cc TEXT, name TEXT) WITHOUT ROWID;
		CREATE TABLE meta (k TEXT PRIMARY KEY, v TEXT)`); err != nil {
		return 0, err
	}
	br := bufio.NewReaderSize(r, 64<<10)
	var total int64
	bad, n := 0, 0
	var batch []geoRow
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		stmt, err := tx.Prepare("INSERT OR REPLACE INTO ranges VALUES (?, ?, ?, ?, ?)")
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		for _, row := range batch {
			if _, err := stmt.Exec(row.start, row.end, row.asn, row.cc, row.name); err != nil {
				stmt.Close()
				_ = tx.Rollback()
				return err
			}
		}
		stmt.Close()
		n += len(batch)
		batch = batch[:0]
		return tx.Commit()
	}
	for {
		line, err := readLine(br)
		if len(line) > 0 {
			total += int64(len(line))
			if total > ipgeoMaxTable {
				return 0, errors.New("the table unpacks to more than the real one")
			}
			parts := strings.Split(strings.TrimRight(line, "\r\n"), "\t")
			var row geoRow
			ok := len(parts) >= 5
			if ok {
				row.start, row.end = Key(parts[0]), Key(parts[1])
				asn, perr := strconv.ParseInt(parts[2], 10, 64)
				row.asn = asn
				ok = row.start != "" && row.end != "" && perr == nil && asn >= 0
			}
			if !ok {
				bad++
				if bad > ipgeoMaxBad {
					return 0, errors.New("too many lines that are not table rows")
				}
			} else if row.asn != 0 {
				cc := strings.ToUpper(strings.TrimSpace(parts[3]))
				if len(cc) != 2 || !isAlpha(cc) {
					cc = ""
				}
				row.cc = cc
				row.name = strings.TrimSpace(parts[4])
				if len(row.name) > 64 {
					row.name = row.name[:64]
				}
				if n+len(batch) >= ipgeoMaxRows {
					return 0, errors.New("the table has more ranges than the real one")
				}
				batch = append(batch, row)
				if len(batch) >= ipgeoBatchCount {
					if err := flush(); err != nil {
						return 0, err
					}
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}
	}
	if err := flush(); err != nil {
		return 0, err
	}
	if n < ipgeoMinRows {
		return 0, errors.New("the table is too small to be the real one")
	}
	_, err := db.Exec("INSERT INTO meta VALUES ('rows', ?), ('built_at', ?)", strconv.Itoa(n), time.Now().UTC().Format(time.RFC3339))
	return n, err
}

var errLongLine = errors.New("a line is longer than any in the real table")

func readLine(br *bufio.Reader) (string, error) {
	var b strings.Builder
	for {
		chunk, isPrefix, err := br.ReadLine()
		b.Write(chunk)
		if b.Len() > ipgeoMaxLine {
			return "", errLongLine
		}
		if err != nil {
			return b.String(), err
		}
		if !isPrefix {
			return b.String(), nil
		}
	}
}

func isAlpha(s string) bool {
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}
