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

	devPorts := in.Config.TunnelPorts()

	serviceNames := sortedKeys(in.Config.Services)
	for _, name := range serviceNames {
		svc := in.Config.Services[name]
		c := buildContainer(name, svc)
		c.TunnelPort = devPorts[name]
		model.Containers = append(model.Containers, c)

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

	// Jobs reuse an existing service's image/networks/env — they never get
	// their own capability network or volume, so they are built after the
	// services loop above (which already created the referenced service's
	// network) and appended directly to Containers/Timers without touching
	// Networks/Volumes.
	jobNames := sortedKeys(in.Config.Jobs)
	for _, name := range jobNames {
		job := in.Config.Jobs[name]
		svc, ok := in.Config.Services[job.Service]
		if !ok {
			// schema.Validate already guarantees job.Service matches a
			// services: key, so this is unreachable outside of tests that
			// build a RuntimeModel directly from an unvalidated config.
			continue
		}
		model.Containers = append(model.Containers, buildJobContainer(name, job, svc))
		model.Timers = append(model.Timers, TimerModel{
			Name:       fmt.Sprintf("ownbase-job-%s", name),
			OnCalendar: job.Schedule,
			Persistent: job.EffectivePersistent(),
		})
	}

	// All containers also join the shared internal management network.
	// Ensure the corresponding .network Quadlet file is generated.
	if len(model.Containers) > 0 && !hasNetwork(model.Networks, "ownbase-internal") {
		model.Networks = append(model.Networks, NetworkModel{Name: "ownbase-internal"})
	}

	// Caddy routes: one per effective domain for every container that has
	// both a domain and a port. Backends are addressed by Podman container
	// name (not "localhost") because Caddy runs isolated on the
	// ownbase-internal network and cannot reach host-loopback ports.
	for _, c := range model.Containers {
		if len(c.PublicDomains) == 0 || c.PublicPort == 0 {
			continue
		}
		for _, domain := range c.PublicDomains {
			model.Routes = append(model.Routes, RouteModel{
				Host:     domain,
				Upstream: fmt.Sprintf("%s:%d", c.Name, c.PublicPort),
			})
		}
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

	// Internal services have domains for tunnel routing but must not
	// receive a Caddy route, so PublicDomains is left nil.
	var publicDomains []string
	if !svc.Internal {
		publicDomains = svc.EffectiveDomains()
	}

	c := ContainerModel{
		Name:          containerName,
		Internal:      svc.Internal,
		PublicDomains: publicDomains,
		PublicPort:    svc.Port,
		Env:           svc.Env,
	}

	// Every service builds from a read-only local bare clone of its repo: URL,
	// stored at /opt/ownbase/repos/<service-name>. The service name keys the
	// local repo directory (BuildSource), so it is collision-free even when
	// two services share the same upstream URL.
	c.Image = fmt.Sprintf("localhost/ownbase-%s:local", name)
	c.BuildSource = name
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

// buildJobContainer compiles one jobs: entry into a ContainerModel. It reuses
// buildContainer for the referenced service to inherit the exact same image
// reference, networks, and hardening (user/capabilities/security-opt) the
// service itself gets, then overrides the pieces that make it a job: a
// distinct "ownbase-job-<name>" container name, no build step of its own (the
// service's own build produces the image), no Caddy route/tunnel port/health
// probe (jobs are not servers), no volume mounts (v1 jobs are ephemeral —
// see JobDecl doc comment), and job env layered after the service's own env:.
func buildJobContainer(name string, job schema.JobDecl, svc schema.ServiceDecl) ContainerModel {
	c := buildContainer(job.Service, svc)
	c.Name = fmt.Sprintf("ownbase-job-%s", name)
	c.IsJob = true
	c.JobService = job.Service
	c.Command = job.Command

	env := make([]string, 0, len(svc.Env)+len(job.Env))
	env = append(env, svc.Env...)
	env = append(env, job.Env...)
	c.Env = env

	// A job's image comes entirely from its service's own build, so the job
	// unit itself carries no build provenance.
	c.BuildSource = ""
	c.BuildRef = ""
	c.BuildDockerfile = ""
	c.BuildContext = ""

	// Jobs are never servers: no Caddy route, no tunnel/loopback publish, no
	// health probe (waitForContainer would otherwise wait for a HTTP 2xx that
	// a batch script never serves). TunnelPort is left at its zero value —
	// build() only assigns one to services in schema.TunnelPorts(), which
	// jobs are not part of.
	c.PublicDomains = nil
	c.PublicPort = 0
	c.HealthProbe = nil

	// v1 jobs don't get volume mounts — reusing the service's own data volume
	// here would mount it into a second container unexpectedly. Add
	// volumes: support to JobDecl if a job later needs durable storage.
	c.VolumeMounts = nil

	return c
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
