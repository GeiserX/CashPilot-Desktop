package macvm

// Pin is one download CashPilot checks before it uses it: the file at URL must
// hash to SHA256, or it is thrown away.
type Pin struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
	// Size is the expected byte count, when known, for progress and the disk check.
	Size int64 `json:"size,omitempty"`
}

// The tools are downloaded on demand from their own releases, never bundled
// (ADR decision 2). The SHA-256 values are the ones each release's
// checksums.txt lists, checked again on our download.
var (
	// TartPin is Tart, the VM manager (FSL-1.1-ALv2; allows non-competing use).
	TartPin = Pin{
		Name: "tart", Version: "2.42.0",
		URL:    "https://github.com/cirruslabs/tart/releases/download/2.42.0/tart.tar.gz",
		SHA256: "a95dda214b5f676298430d665a8174992e6b300dd20c653385b60481ae819ddf",
	}
	// SoftnetPin is softnet, Tart's packet filter (FSL-1.1-ALv2). It needs
	// macOS 26 or newer.
	SoftnetPin = Pin{
		Name: "softnet", Version: "0.26.0",
		URL:    "https://github.com/cirruslabs/softnet/releases/download/0.26.0/softnet.tar.gz",
		SHA256: "8469759f9cdfaaba52a150b629877985129308461702bc074c3ad7727837db82",
	}
	// AgentPin is the guest agent: the only way the host reaches the guest.
	AgentPin = Pin{
		Name: "tart-guest-agent", Version: "0.15.0",
		URL:    "https://github.com/cirruslabs/tart-guest-agent/releases/download/v0.15.0/tart-guest-agent-darwin-all.tar.gz",
		SHA256: "eb47f402f18e742a8ea96344115ca776179f9c4c67494bc0c46a10a68db280b7",
	}
	// IPSWPin is Apple's own macOS restore image for virtual Macs. A guest
	// older than the host runs; a newer one does not, so the host needs
	// macOS 26 or newer.
	IPSWPin = Pin{
		Name: "macOS", Version: "26.6.2 (25G83)",
		URL:    "https://updates.cdn-apple.com/2026SummerFCS/fullrestores/140-75212/A2A24B94-1FC1-45A3-93F7-C51B02AF1F4D/UniversalMac_26.6.2_25G83_Restore.ipsw",
		SHA256: "885503b7f4b06609e9a512f2befd40f59730640a3f1233e3892d60affdd51c95",
		Size:   19772231540,
	}
)

// MinHostMajor is the oldest macOS the host may run: softnet 0.26 needs 26,
// and the pinned guest is 26.
const MinHostMajor = 26

// The guest's size. The lab measured 6 GB of memory, about 1.4 cores and
// 38 GB of a sparse 80 GB disk with five earners.
const (
	GuestCPUs     = 4
	GuestMemoryMB = 6144
	GuestDiskGB   = 80
)

// Host needs before a create starts.
const (
	// NeedRAM is the Mac's total memory the guest needs: its own 6 GB and
	// room for macOS and the apps beside it. The guest's memory is taken as it
	// is used, so the free memory of the moment is no test; the total is.
	NeedRAM = int64(16) << 30
	// NeedFreeDisk is the restore image, the installed guest and headroom,
	// on the volume that holds the Tart folder.
	NeedFreeDisk = int64(60) << 30
	// LowDisk is the free space below which the status warns: the guest's
	// sparse disk grows into it, and a full host volume stalls the guest.
	LowDisk = int64(10) << 30
)
