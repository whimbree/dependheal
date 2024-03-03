package main

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	containerType "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const DEPENDHEAL_ENABLE_ALL_ENVAR = "DEPENDHEAL_ENABLE_ALL"
const DEBUG_ENVAR = "DEBUG"

type ContainerData struct {
	ID       string
	Name     string
	Labels   map[string]string
	Networks []string
}

type RestartContext struct {
	containerID         string
	containerName       string
	parentContainerName string
}

func restartChild(cli *client.Client, ctx context.Context, restartContext RestartContext) {
	if restartContext.parentContainerName != "" {
		log.Info().Msgf("Restarting container: %s, depends on: %s", restartContext.containerName, restartContext.parentContainerName)
	} else {
		log.Info().Msgf("Restarting container: %s", restartContext.containerName)
	}

	max_tries := 3
	for i := 0; i < max_tries; i++ {
		if err := cli.ContainerRestart(ctx, restartContext.containerID, containerType.StopOptions{}); err == nil {
			break
		}
		log.Error().Msgf("Error when restarting container: %s, attempt: %d", restartContext.containerName, i+1)
	}
}

func restartChildren(cli *client.Client, ctx context.Context, RestartContexts []RestartContext) {
	for _, RestartContext := range RestartContexts {
		go restartChild(cli, ctx, RestartContext)
	}
}

func connectNetwork(cli *client.Client, ctx context.Context, networkID, containerID string) {
	go cli.NetworkConnect(ctx, networkID, containerID, &network.EndpointSettings{})
}

func isContainerUnhealthy(cli *client.Client, ctx context.Context, containerID string) (bool, error) {
	containerJSON, err := cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return false, err
	}
	// If we have a healthcheck and the type is unhealthy, return true
	return containerJSON.State.Health != nil && containerJSON.State.Health.Status == types.Unhealthy, nil
}

func delayedRestart(cli *client.Client, ctx context.Context, restartContext RestartContext, timeout float64) {
	if timeout != 0 {
		log.Info().Msgf("Waiting for timeout of %.1f seconds before restarting %s", timeout, restartContext.containerName)
	}
	time.Sleep(time.Duration(timeout) * time.Second)
	restartChild(cli, ctx, restartContext)
}

func getNetworkNameIDMapping(cli *client.Client, ctx context.Context) map[string]string {
	// Create a mapping between networkName and networkID
	networkNameIDMapping := make(map[string]string)
	networks, _ := cli.NetworkList(ctx, types.NetworkListOptions{})
	for _, network := range networks {
		log.Debug().Msgf("Network: %s, id: %s", network.Name, network.ID)
		networkNameIDMapping[network.Name] = network.ID
	}
	return networkNameIDMapping
}

func parseCommaSeperatedList(stringList string) []string {
	var returnList []string
	splitStringList := strings.Split(stringList, ",")
	for _, item := range splitStringList {
		item = strings.TrimSpace(item)
		if len(item) != 0 {
			returnList = append(returnList, item)
		}
	}
	return returnList
}

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})
	zerolog.SetGlobalLevel(zerolog.InfoLevel)
	if debug_envar, ok := os.LookupEnv(DEBUG_ENVAR); ok {
		debug, err := strconv.ParseBool(debug_envar)
		if err != nil {
			log.Error().Msgf("Expected boolean for environment variable %s, provided %s", DEBUG_ENVAR, debug_envar)
		} else if debug {
			zerolog.SetGlobalLevel(zerolog.DebugLevel)
			log.Info().Msgf("Environment variable %s=true, debug logging enabled", DEBUG_ENVAR)
		}
	}

	cli, err := client.NewClientWithOpts(client.WithHost("unix:///var/run/docker.sock"))
	if err != nil {
		panic(err)
	}

	ctx := context.Background()

	cli.NegotiateAPIVersion(ctx)

	containers, err := cli.ContainerList(ctx, types.ContainerListOptions{})
	if err != nil {
		panic(err)
	}

	enable_all := false
	if enable_all_envar, ok := os.LookupEnv(DEPENDHEAL_ENABLE_ALL_ENVAR); ok {
		enable_all, err = strconv.ParseBool(enable_all_envar)
		if err != nil {
			log.Error().Msgf("Expected boolean for environment variable %s, provided %s", DEPENDHEAL_ENABLE_ALL_ENVAR, enable_all_envar)
		}
	}
	if enable_all {
		log.Info().Msgf("Environment variable %s=true, watching all containers", DEPENDHEAL_ENABLE_ALL_ENVAR)
	}

	// Find all containers that have dependheal.enable = true
	watchedContainers := make(map[string]ContainerData)
	unhealthyContainers := make([]RestartContext, 0)
	for _, container := range containers {
		if enable_all || hasLabel(container.Labels, "dependheal.enable", "true") {
			name := strings.TrimPrefix(container.Names[0], "/")
			// Parse dependheal.networks = network1, network2, ...
			var networks []string
			if networklabel, ok := container.Labels["dependheal.networks"]; ok {
				networks = parseCommaSeperatedList(networklabel)
			}
			if !enable_all {
				log.Info().Msgf("Watching container: %s, networks: %v", name, networks)
			} else {
				log.Debug().Msgf("Watching container: %s, networks: %v", name, networks)
			}
			watchedContainers[container.ID] = ContainerData{container.ID, name, container.Labels, networks}
			isUnhealthy, err := isContainerUnhealthy(cli, ctx, container.ID)
			if err != nil {
				log.Error().Msgf("Checking health status of %s failed", name)
				continue
			}
			if isUnhealthy {
				unhealthyContainers = append(unhealthyContainers, RestartContext{container.ID, name, ""})
			}
		}
	}

	// Restart unhealthy containers
	for _, restartContext := range unhealthyContainers {
		log.Info().Msgf("Container unhealthy: %s", restartContext.containerName)
		timeout := getLabelFloat(watchedContainers[restartContext.containerID].Labels, "dependheal.timeout", 0)
		go delayedRestart(cli, ctx, restartContext, timeout)
	}

	networkNameIDMapping := getNetworkNameIDMapping(cli, ctx)

	// Make sure every container is connected to its networks
	for _, container := range watchedContainers {
		for _, network := range container.Networks {
			if networkID, ok := networkNameIDMapping[network]; ok {
				log.Info().Msgf("Connecting container: %s to network: %s", container.Name, network)
				connectNetwork(cli, ctx, networkID, container.ID)
			}
		}
	}

	// Listen for Docker events and act on them
	eventFilter := filters.NewArgs()
	eventFilter.Add("type", "container")
	eventFilter.Add("event", "start")
	eventFilter.Add("event", "stop")
	eventFilter.Add("event", "die")
	eventFilter.Add("event", "health_status")

	eventChan, eventErrChan := cli.Events(ctx, types.EventsOptions{Filters: eventFilter})

	// Store mapping of parent container ID -> [children that should be restarted] once the parent is healthy
	childRestartOnParentHealthy := make(map[string][]RestartContext)

	for {
		select {
		case event := <-eventChan:
			if event.Type == "container" {
				if event.Action == "start" {
					// Update networkName -> networkID mapping
					networkNameIDMapping = getNetworkNameIDMapping(cli, ctx)
					// Check if started container has dependheal.enable = true
					if enable_all || hasLabel(event.Actor.Attributes, "dependheal.enable", "true") {
						// Parse dependheal.networks = network1, network2, ...
						var networks []string
						if networklabel, ok := event.Actor.Attributes["dependheal.networks"]; ok {
							networks = parseCommaSeperatedList(networklabel)
						}
						parentContainer := ContainerData{event.Actor.ID, event.Actor.Attributes["name"], event.Actor.Attributes, networks}
						watchedContainers[parentContainer.ID] = parentContainer
						log.Info().Msgf("Container started: %s, networks: %v", parentContainer.Name, parentContainer.Networks)

						// Make sure container is connected to its networks
						for _, network := range parentContainer.Networks {
							if networkID, ok := networkNameIDMapping[network]; ok {
								log.Info().Msgf("Connecting container: %s to network: %s", parentContainer.Name, network)
								connectNetwork(cli, ctx, networkID, parentContainer.ID)
							}
						}

						children := make([]RestartContext, 0)
						childrenOnHealthy := make([]RestartContext, 0)

						for _, container := range watchedContainers {
							// Find all containers that have dependheal.parent = <PARENT_NAME>
							if container.ID != parentContainer.ID && hasLabel(container.Labels, "dependheal.parent", parentContainer.Name) {
								restartContext := RestartContext{container.ID, container.Name, parentContainer.Name}
								// If dependheal.wait_for_parent_healthy = true, schedule restart once parent container is healthy
								// Else restart chilren immediately
								if hasLabel(container.Labels, "dependheal.wait_for_parent_healthy", "true") {
									childrenOnHealthy = append(childrenOnHealthy, restartContext)
								} else {
									children = append(children, restartContext)
								}
							}
						}
						// Schedule restarts once parent is healthy
						childRestartOnParentHealthy[parentContainer.ID] = childrenOnHealthy
						// Restart children immediately
						restartChildren(cli, ctx, children)
					}
				}
				if parentContainer, ok := watchedContainers[event.Actor.ID]; ok {
					if event.Action == "stop" || event.Action == "die" {
						log.Info().Msgf("Container stopped: %s", parentContainer.Name)
						delete(watchedContainers, parentContainer.ID)
					}

					if event.Action == "health_status: healthy" {
						log.Info().Msgf("Container healthy: %s", parentContainer.Name)
						if childrenToRestart, ok := childRestartOnParentHealthy[parentContainer.ID]; ok {
							restartChildren(cli, ctx, childrenToRestart)
							delete(childRestartOnParentHealthy, parentContainer.ID)
						}
					}
					if event.Action == "health_status: unhealthy" {
						log.Info().Msgf("Container unhealthy: %s", parentContainer.Name)
						timeout := getLabelFloat(parentContainer.Labels, "dependheal.timeout", 0)
						go delayedRestart(cli, ctx, RestartContext{parentContainer.ID, parentContainer.Name, ""}, timeout)
					}
				}
			}

		case err := <-eventErrChan:
			if err != nil {
				panic(err)
			}
		}
	}
}

// Helper function to check if a label value matches an expected value
func hasLabel(labels map[string]string, key string, value string) bool {
	if val, ok := labels[key]; ok {
		return val == value
	}
	return false
}

// Helper function to get an decimal value from a label
func getLabelFloat(labels map[string]string, key string, defaultValue float64) float64 {
	if val, ok := labels[key]; ok {
		// Attempt to parse the input string as a floating point number
		floatVal, err := strconv.ParseFloat(val, 64)
		if err == nil {
			return floatVal
		}
		// Attempt to parse the input string as an integer
		intVal, err := strconv.Atoi(val)
		if err == nil {
			return float64(intVal)
		}
		// Neither parsing attempt was successful
		return defaultValue
	}
	return defaultValue
}
