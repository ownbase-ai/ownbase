package compiler

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ownbase/ownbase/internal/schema"
)

// build assembles the complete RuntimeModel from the compiler Input.
// Pure data transformation: no I/O, no clock, no hostname, no randomness.
// Determinism is guaranteed by sorting every collection before appending.
func build(in Input) RuntimeModel {
	model := RuntimeModel{}

	serviceNames := sortedKeys(in.Config.Services)
	for _, name := range serviceNames {
		svc := in.Config.Services[name]
		model.Containers = append(model.Containers, buildContainer(name, svc))

		// Every service gets its own capability network keyed by service name.
		// Consumers join this network via their requires: list.
		net := NetworkModel{Name: capabilityNetworkName(name)}
		if !hasNetwork(model.Networks, net.Name) {
			model.Networks = append(model.Networks, net)
		}

		if len(svc.Volumes) > 0 {
			for _, v := range svc.Volumes {
				model.Volumes = append(model.Volumes, VolumeModel{
					Name: fmt.Sprintf("ownbase-%s-%s", name, v.Name),
				})
			}
		} else {
			model.Volumes = append(model.Volumes, VolumeModel{
				Name: fmt.Sprintf("ownbase-%s-data", name),
			})
		}
	}

	// All containers also join the shared internal management network.
	// Ensure the corresponding .network Quadlet file is generated.
	if len(model.Containers) > 0 && !hasNetwork(model.Networks, "ownbase-internal") {
		model.Networks = append(model.Networks, NetworkModel{Name: "ownbase-internal"})
	}

	// Caddy routes: one per container that has both domain and port.
	for _, c := range model.Containers {
		if c.PublicDomain == "" || c.PublicPort == 0 {
			continue
		}
		model.Routes = append(model.Routes, RouteModel{
			Host:     c.PublicDomain,
			Upstream: fmt.Sprintf("localhost:%d", c.PublicPort),
		})
	}

	model.ACMEEmail = in.Config.Core.Caddy.Email

	sort.Slice(model.Routes, func(i, j int) bool {
		return model.Routes[i].Host < model.Routes[j].Host
	})
	sort.Slice(model.Networks, func(i, j int) bool {
		return model.Networks[i].Name < model.Networks[j].Name
	})
	sort.Slice(model.Volumes, func(i, j int) bool {
		return model.Volumes[i].Name < model.Volumes[j].Name
	})

	return model
}

func buildContainer(name string, svc schema.ServiceDecl) ContainerModel {
	containerName := fmt.Sprintf("ownbase-%s", name)
	dataVolumeName := fmt.Sprintf("ownbase-%s-data", name)

	c := ContainerModel{
		Name:         containerName,
		PublicDomain: svc.Domain,
		PublicPort:   svc.Port,
		Env:          svc.Env,
	}

	// All user services build from a local bare repo under /opt/ownbase/repos/.
	// source: uses the path directly; mirror: derives the name as mirrors-<basename>.
	buildSource := svc.Source
	if buildSource == "" && svc.Mirror != "" {
		buildSource = MirrorRepoName(svc.Mirror)
	}
	c.Image = fmt.Sprintf("localhost/ownbase-%s:local", name)
	c.BuildSource = buildSource
	c.BuildRef = svc.Ref
	c.BuildDockerfile = svc.Dockerfile
	c.BuildContext = svc.Context

	// Persistent volumes. When Volumes is declared, use those; otherwise fall
	// back to the single data volume for backward compatibility.
	if len(svc.Volumes) > 0 {
		for _, v := range svc.Volumes {
			volName := fmt.Sprintf("ownbase-%s-%s", name, v.Name)
			c.VolumeMounts = append(c.VolumeMounts, VolumeMount{VolumeName: volName, MountPath: v.Mount})
		}
	} else {
		dataPath := svc.DataPath
		if dataPath == "" {
			dataPath = "/data"
		}
		c.VolumeMounts = []VolumeMount{{VolumeName: dataVolumeName, MountPath: dataPath}}
	}

	// Health probe from ownbase.yaml.
	if svc.HealthProbe != nil && svc.HealthProbe.HTTP != "" {
		c.HealthProbe = &HealthProbeModel{HTTPPath: svc.HealthProbe.HTTP}
	}

	// Requires: service names this container depends on (sorted for determinism).
	if len(svc.Requires) > 0 {
		c.Requires = make([]string, len(svc.Requires))
		copy(c.Requires, svc.Requires)
		sort.Strings(c.Requires)
	}

	// Every service joins its own capability network. This makes the container
	// reachable by its service name from any consumer that also joins this
	// network via requires:. Without this, the provider would be unreachable
	// even though the network exists.
	ownNet := capabilityNetworkName(name)
	if !containsString(c.Networks, ownNet) {
		c.Networks = append(c.Networks, ownNet)
	}

	// Join the capability network of each required service so the consumer
	// can reach the provider by hostname.
	for _, cap := range svc.Requires {
		capNet := capabilityNetworkName(cap)
		if !containsString(c.Networks, capNet) {
			c.Networks = append(c.Networks, capNet)
		}
	}

	// Always join the shared internal management network.
	if !containsString(c.Networks, "ownbase-internal") {
		c.Networks = append(c.Networks, "ownbase-internal")
	}
	sort.Strings(c.Networks)

	// Security: propagate user, capability, and security-opt overrides from ownbase.yaml.
	c.User = svc.User
	if len(svc.AddCapabilities) > 0 {
		c.AddCapabilities = make([]string, len(svc.AddCapabilities))
		copy(c.AddCapabilities, svc.AddCapabilities)
	}
	if len(svc.SecurityOpt) > 0 {
		c.SecurityOpts = make([]string, len(svc.SecurityOpt))
		copy(c.SecurityOpts, svc.SecurityOpt)
	}

	return c
}

// MirrorRepoName derives the local bare-repo name for an external git mirror
// URL. The convention is "mirrors-<basename>", stored at
// /opt/ownbase/repos/mirrors-<basename>. Using a dash (not a slash) keeps the
// repo name flat — no nested directories required.
//
// Examples:
//
//	"https://github.com/docker-library/postgres" → "mirrors-postgres"
//	"https://github.com/org/crm.git"             → "mirrors-crm"
//	"git@github.com:org/myapp.git"               → "mirrors-myapp"
func MirrorRepoName(mirrorURL string) string {
	u := strings.TrimRight(mirrorURL, "/")
	u = strings.TrimSuffix(u, ".git")
	// Handle git@host:org/repo form.
	if idx := strings.Index(u, ":"); idx >= 0 && !strings.Contains(u[:idx], "/") {
		u = u[idx+1:]
	}
	idx := strings.LastIndex(u, "/")
	if idx < 0 {
		return "mirrors-" + u
	}
	return "mirrors-" + u[idx+1:]
}

func capabilityNetworkName(serviceName string) string {
	return fmt.Sprintf("ownbase-%s-net", strings.ToLower(serviceName))
}

func hasNetwork(networks []NetworkModel, name string) bool {
	for _, n := range networks {
		if n.Name == name {
			return true
		}
	}
	return false
}

func containsString(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
