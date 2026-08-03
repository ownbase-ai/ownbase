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
		n := svc.ReplicaCount()
		for i := 0; i < n; i++ {
			c := buildContainer(name, svc, i)
			c.TunnelPort = devPorts[c.Name]
			model.Containers = append(model.Containers, c)
		}

		// Every service gets its own capability network keyed by service name.
		// Consumers join this network via their requires: list. Replicas share
		// one network — the capability name is the service key, not a member.
		net := NetworkModel{Name: capabilityNetworkName(name)}
		if !hasNetwork(model.Networks, net.Name) {
			model.Networks = append(model.Networks, net)
		}

		model.Volumes = append(model.Volumes, buildVolumes(name, svc)...)
	}

	// Jobs reuse an existing service's image/networks/env — they never get
	// their own capability network or volume, so they are built after the
	// services loop above (which already created the referenced service's
	// network) and appended directly to Containers/Timers without touching
	// Networks/Volumes. Jobs are never multiplied by replicas.
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

	// Caddy routes: one per effective domain, with all replica upstreams
	// for containers that share that domain. Backends are addressed by
	// Podman container name (not "localhost") because Caddy runs isolated
	// on the ownbase-internal network and cannot reach host-loopback ports.
	model.Routes = buildRoutes(model.Containers)

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

// buildRoutes collapses replica containers of the same service that share a
// public domain into one RouteModel with every replica as an upstream.
// Upstreams are never merged across different services — two apps claiming
// the same domain is a config error (schema validation) and must not become
// a silent cross-app load-balance pool.
func buildRoutes(containers []ContainerModel) []RouteModel {
	// host → service + upstreams (service pins the first claimant)
	type pair struct {
		host      string
		service   string
		upstreams map[string]bool
	}
	byHost := make(map[string]*pair)
	var hosts []string
	for _, c := range containers {
		if c.IsJob || len(c.PublicDomains) == 0 || c.PublicPort == 0 {
			continue
		}
		upstream := fmt.Sprintf("%s:%d", c.Name, c.PublicPort)
		// ServiceName is empty for core packages; fall back to container name
		// so two unrelated empties never merge by accident.
		svc := c.ServiceName
		if svc == "" {
			svc = c.Name
		}
		for _, domain := range c.PublicDomains {
			p, ok := byHost[domain]
			if !ok {
				p = &pair{host: domain, service: svc, upstreams: make(map[string]bool)}
				byHost[domain] = p
				hosts = append(hosts, domain)
			} else if p.service != svc {
				// Different service already owns this host — skip. Validate()
				// rejects this config; defensive so a bypass cannot LB-merge.
				continue
			}
			p.upstreams[upstream] = true
		}
	}
	sort.Strings(hosts)
	routes := make([]RouteModel, 0, len(hosts))
	for _, host := range hosts {
		p := byHost[host]
		ups := make([]string, 0, len(p.upstreams))
		for u := range p.upstreams {
			ups = append(ups, u)
		}
		sort.Strings(ups)
		routes = append(routes, RouteModel{Host: host, Upstreams: ups})
	}
	return routes
}

// buildVolumes returns the VolumeModels for one service (shared and/or
// per-replica instances).
func buildVolumes(name string, svc schema.ServiceDecl) []VolumeModel {
	var out []VolumeModel
	if len(svc.Volumes) > 0 {
		for _, v := range svc.Volumes {
			if svc.VolumeIsPerReplica(v) {
				for i := 0; i < svc.ReplicaCount(); i++ {
					out = append(out, VolumeModel{
						Name: schema.VolumeName(name, v.Name, svc.Replicas, i, true),
					})
				}
			} else {
				out = append(out, VolumeModel{
					Name: schema.VolumeName(name, v.Name, svc.Replicas, 0, false),
				})
			}
		}
		return out
	}
	// data_path shorthand
	if svc.DataPathIsPerReplica() {
		for i := 0; i < svc.ReplicaCount(); i++ {
			out = append(out, VolumeModel{
				Name: schema.DataVolumeName(name, svc.Replicas, i, true),
			})
		}
	} else {
		out = append(out, VolumeModel{
			Name: schema.DataVolumeName(name, svc.Replicas, 0, false),
		})
	}
	return out
}

// buildContainer compiles one service instance (replica index i, or the
// single unindexed instance when replicas is absent — then i must be 0).
func buildContainer(name string, svc schema.ServiceDecl, index int) ContainerModel {
	containerName := schema.ContainerName(name, svc.Replicas, index)

	// Internal services have domains for tunnel routing but must not
	// receive a Caddy route, so PublicDomains is left nil.
	var publicDomains []string
	if !svc.Internal {
		publicDomains = svc.EffectiveDomains()
	}

	replicaIndex := -1
	replicaCount := 1
	if svc.IsReplicated() {
		replicaIndex = index
		replicaCount = svc.ReplicaCount()
	}

	c := ContainerModel{
		Name:          containerName,
		ServiceName:   name,
		ReplicaIndex:  replicaIndex,
		ReplicaCount:  replicaCount,
		Internal:      svc.Internal,
		PublicDomains: publicDomains,
		PublicPort:    svc.Port,
		Env:           append([]string(nil), svc.Env...),
	}

	// Every service builds from a read-only local bare clone of its repo URL,
	// stored at /opt/ownbase/repos/<service-name>. The service name keys the
	// local repo directory (BuildSource), so it is collision-free even when
	// two services share the same upstream URL. All replicas share one image.
	c.Image = fmt.Sprintf("localhost/ownbase-%s:local", name)
	c.BuildSource = name
	c.BuildRef = svc.Ref
	c.BuildDockerfile = svc.Dockerfile
	c.BuildContext = svc.Context

	// Persistent volumes. When Volumes is declared, use those; otherwise fall
	// back to the single data volume for backward compatibility.
	if len(svc.Volumes) > 0 {
		for _, v := range svc.Volumes {
			perReplica := svc.VolumeIsPerReplica(v)
			volName := schema.VolumeName(name, v.Name, svc.Replicas, index, perReplica)
			c.VolumeMounts = append(c.VolumeMounts, VolumeMount{VolumeName: volName, MountPath: v.Mount})
		}
	} else {
		dataPath := svc.DataPath
		if dataPath == "" {
			dataPath = "/data"
		}
		perReplica := svc.DataPathIsPerReplica()
		volName := schema.DataVolumeName(name, svc.Replicas, index, perReplica)
		c.VolumeMounts = []VolumeMount{{VolumeName: volName, MountPath: dataPath}}
	}

	// Replica identity — only when replicas: is set, so non-replicated units
	// stay byte-identical to configs that predate the field.
	if svc.IsReplicated() {
		c.Env = append(c.Env,
			fmt.Sprintf("OWNBASE_REPLICA_INDEX=%d", index),
			fmt.Sprintf("OWNBASE_REPLICA_COUNT=%d", svc.ReplicaCount()),
		)
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
	// reachable by its container name from any consumer that also joins this
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
	if svc.Resources != nil {
		c.MemoryLimit = svc.Resources.Memory
		c.CPULimit = svc.Resources.CPUs
	}

	return c
}

// buildJobContainer compiles one jobs: entry into a ContainerModel. It reuses
// the referenced service's image, networks, env, and hardening, then overrides
// the fields that make a job a job (oneshot, command, no route/probe/volumes).
// Jobs are never multiplied by the service's replicas: count.
func buildJobContainer(name string, job schema.JobDecl, svc schema.ServiceDecl) ContainerModel {
	// Build from the unindexed / primary shape of the service for image and
	// networks, then strip volume mounts and public routing. Replica env is
	// not inherited — a job is not a replica.
	base := buildContainer(job.Service, svc, 0)
	// Force unindexed job naming regardless of service replicas.
	c := base
	c.Name = fmt.Sprintf("ownbase-job-%s", name)
	c.ServiceName = ""
	c.ReplicaIndex = -1
	c.ReplicaCount = 1
	c.IsJob = true
	c.JobService = job.Service
	c.Command = append([]string(nil), job.Command...)
	c.PublicDomains = nil
	c.PublicPort = 0
	c.TunnelPort = 0
	c.HealthProbe = nil
	c.VolumeMounts = nil
	// Image comes entirely from the service's own build — the job unit
	// itself carries no build provenance.
	c.BuildSource = ""
	c.BuildRef = ""
	c.BuildDockerfile = ""
	c.BuildContext = ""
	// Rebuild env from service + job (no replica identity).
	env := make([]string, 0, len(svc.Env)+len(job.Env))
	env = append(env, svc.Env...)
	env = append(env, job.Env...)
	c.Env = env
	return c
}

func capabilityNetworkName(serviceName string) string {
	return fmt.Sprintf("ownbase-%s-net", strings.ToLower(serviceName))
}

func hasNetwork(nets []NetworkModel, name string) bool {
	for _, n := range nets {
		if n.Name == name {
			return true
		}
	}
	return false
}

func containsString(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
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
