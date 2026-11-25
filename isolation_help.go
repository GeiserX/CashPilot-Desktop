package main

import (
	"strings"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// isolationHelpBase is the LAN isolation setting's help before the sentence on files.
const isolationHelpBase = "auto (default), on or off. auto fences container earners off your LAN, this computer, a tailnet and cloud metadata on a new install, and leaves an upgraded one as it is until you turn isolation on from the dashboard. off deploys on Docker's default bridge, where earners can reach your LAN. Podman cannot be fenced yet: there auto deploys unfenced and on refuses container deploys. Takes effect on the next deploy."

// isolationHelp is the LAN isolation help with what a container earner can read on
// disk. The fence is about the network; a person also asks about their files. A
// container sees only what the deploy mounts into it (internal/runtime buildMounts):
// a named volume of its own, or a folder of this computer. The services that mount
// a folder are read from the catalog, so the sentence follows it.
func isolationHelp(services []catalog.Service) string {
	files := "On disk, a container earner sees only its own data volume, which CashPilot mounts into it, and none of your files."
	if names := hostFolderServices(services); len(names) > 0 {
		files = "On disk, a container earner sees only what CashPilot mounts into it: its own data volume, and for " +
			joinNames(names) + " the folders you choose for " + pronoun(len(names)) + ". It does not see the rest of your files."
	}
	return isolationHelpBase + " " + files + " An app that runs outside Docker is not fenced and can read what your account can."
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
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

func pronoun(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}
