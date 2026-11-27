package main

import (
	"strings"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/i18n"
)

// isolationHelp is the LAN isolation help with what a container earner can read on
// disk. The fence is about the network; a person also asks about their files. A
// container sees only what the deploy mounts into it (internal/runtime buildMounts):
// a named volume of its own, or a folder of this computer. The services that mount
// a folder are read from the catalog, so the sentence follows it.
func isolationHelp(services []catalog.Service) string {
	files := i18n.T("go.app.isolationHelp.filesVolumeOnly")
	if names := hostFolderServices(services); len(names) > 0 {
		files = i18n.T("go.app.isolationHelp.filesFolders", "names", joinNames(names), "n", len(names))
	}
	return i18n.T("go.app.isolationHelp.message", "base", i18n.T("go.app.isolationHelp.base"), "files", files)
}

// hostFolderServices names the container services whose deploy mounts a folder of
// this computer, in catalog order.
func hostFolderServices(services []catalog.Service) []string {
	var names []string
	for _, svc := range services {
		if svc.Docker.Image == "" {
			continue
		}
		for _, volume := range svc.Docker.Volumes {
			if mountsHostFolder(volume) {
				name := svc.Name
				if name == "" {
					name = svc.Slug
				}
				names = append(names, name)
				break
			}
		}
	}
	return names
}

// mountsHostFolder reports a "source:target" volume whose source is a folder of
// this computer: an absolute or relative path, or a ${VAR} the person fills in with
// one. Anything else is a named volume Docker keeps for the container. The same
// split as internal/runtime isNamedVolume, read before substitution.
func mountsHostFolder(volume string) bool {
	source, _, ok := strings.Cut(volume, ":")
	if !ok || source == "" {
		return false
	}
	return strings.HasPrefix(source, "/") || strings.HasPrefix(source, ".") || strings.HasPrefix(source, "~") || strings.Contains(source, "${")
}

func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	rest := strings.Join(names[:len(names)-1], i18n.T("go.app.list.separator"))
	return i18n.T("go.app.list.and", "rest", rest, "last", names[len(names)-1])
}
