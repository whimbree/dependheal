package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	containerType "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
)

const DEPENDHEAL_ENABLE_ALL_ENVAR = "DEPENDHEAL_ENABLE_ALL"

type ContainerData struct {
	ID     string
	Name   string
	Labels map[string]string
}

type RestartContext struct {
	containerID         string
	containerName       string
	parentContainerName string
}

func restartChild(cli *client.Client, ctx context.Context, restartContext RestartContext) {
	if restartContext.parentContainerName != "" {
		fmt.Printf("Restarting container: %s, depends on: %s\n", restartContext.containerName, restartContext.parentContainerName)
	} else {
		fmt.Printf("Restarting container: %s\n", restartContext.containerName)
	}

	max_tries := 3
	for i := 0; i < max_tries; i++ {
		if err := cli.ContainerRestart(ctx, restartContext.containerID, containerType.StopOptions{}); err == nil {
			break
		}
		_ = fmt.Errorf("Error when restarting container: %s, attempt: %d\n", restartContext.containerName, i+1)
	}
}

func restartChildren(cli *client.Client, ctx context.Context, RestartContexts []RestartContext) {
	for _, RestartContext := range RestartContexts {
		go restartChild(cli, ctx, RestartContext)
	}
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
		fmt.Printf("Waiting for timeout of %.1f seconds before restarting %s\n", timeout, restartContext.containerName)
	}
	time.Sleep(time.Duration(timeout) * time.Second)
	restartChild(cli, ctx, restartContext)
}

func main() {
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
			_ = fmt.Errorf("Expected boolean for environment variable %s, provided %s", DEPENDHEAL_ENABLE_ALL_ENVAR, enable_all_envar)
		}
	}
	if enable_all {
		fmt.Printf("Environment variable %s=true, watching all containers\n", DEPENDHEAL_ENABLE_ALL_ENVAR)
	}

	// Find all containers that have dependheal.enable = true
	watchedContainers := make(map[string]ContainerData)
	unhealthyContainers := make([]RestartContext, 0)
	for _, container := range containers {
		if enable_all || hasLabel(container.Labels, "dependheal.enable", "true") {
			name := strings.TrimPrefix(container.Names[0], "/")
			fmt.Printf("Watching container: %s\n", name)
			watchedContainers[container.ID] = ContainerData{container.ID, name, container.Labels}
			isUnhealthy, err := isContainerUnhealthy(cli, ctx, container.ID)
			if err != nil {
				_ = fmt.Errorf("Checking health status of %s failed", name)
				continue
			}
			if isUnhealthy {
				unhealthyContainers = append(unhealthyContainers, RestartContext{container.ID, name, ""})
			}
		}
	}

	// Restart unhealthy containers
	for _, restartContext := range unhealthyContainers {
		fmt.Printf("Container unhealthy: %s\n", restartContext.containerName)
		timeout := getLabelFloat(watchedContainers[restartContext.containerID].Labels, "dependheal.timeout", 0)
		go delayedRestart(cli, ctx, restartContext, timeout)
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
					// Check if started container has dependheal.enable = true
					if enable_all || hasLabel(event.Actor.Attributes, "dependheal.enable", "true") {
						parentContainer := ContainerData{event.Actor.ID, event.Actor.Attributes["name"], event.Actor.Attributes}
						fmt.Printf("Container started: %s\n", parentContainer.Name)
						watchedContainers[parentContainer.ID] = parentContainer

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
						fmt.Printf("Container stopped: %s\n", parentContainer.Name)
						delete(watchedContainers, parentContainer.ID)
					}

					if event.Action == "health_status: healthy" {
						fmt.Printf("Container healthy: %s\n", parentContainer.Name)
						if childrenToRestart, ok := childRestartOnParentHealthy[parentContainer.ID]; ok {
							restartChildren(cli, ctx, childrenToRestart)
							delete(childRestartOnParentHealthy, parentContainer.ID)
						}
					}
					if event.Action == "health_status: unhealthy" {
						fmt.Printf("Container unhealthy: %s\n", parentContainer.Name)
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
