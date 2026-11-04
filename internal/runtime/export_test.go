package runtime

// Exported for the external test package (custom_hardening_test.go), which needs the
// services package and so cannot live inside this one.
var (
	BuildHostConfig = buildHostConfig
	BuildMounts     = buildMounts
	BuildPorts      = buildPorts
	ContainerLabels = containerLabels
	BuildEnv        = buildEnv
	BuildCommand    = buildCommandArgs

	UseAmbientDocker = useAmbientDocker
	AssertHardened   = assertHardened
	DockerClient     = dockerClient
	PullImage        = pullImage
)
