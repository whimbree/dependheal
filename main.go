package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/docker/docker/api/types"
	containerType "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
)

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

	// Find all containers that have dependheal.enable = true
	watchedContainers := make(map[string]types.Container)
	for _, container := range containers {
		if hasLabel(container.Labels, "dependheal.enable", "true") {
			name := strings.TrimPrefix(container.Names[0], "/")
			fmt.Printf("Watching container: %s\n", name)
			watchedContainers[container.ID] = container
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

	for {
		select {
		case event := <-eventChan:
			if event.Type == "container" {
				if event.Action == "start" {
					if hasLabel(event.Actor.Attributes, "dependheal.enable", "true") {
						// quick n dirty, I need a types.Container type
						var startedContainer types.Container
						containers, _ := cli.ContainerList(ctx, types.ContainerListOptions{})
						for _, container := range containers {
							if container.ID == event.Actor.ID {
								startedContainer = container
							}
						}
						name := strings.TrimPrefix(startedContainer.Names[0], "/")
						fmt.Printf("Watching container: %s\n", name)
						watchedContainers[event.Actor.ID] = startedContainer

						for _, container := range watchedContainers {
							parentName := strings.TrimPrefix(startedContainer.Names[0], "/")
							if container.ID != startedContainer.ID && hasLabel(container.Labels, "dependheal.parent", parentName) {
								childName := strings.TrimPrefix(container.Names[0], "/")
								if false && hasLabel(container.Labels, "dependheal.wait_for_parent_healthy", "true") {
									// We have to wait for the parent to be healthy, schedule the children to restart once we recieve that event
									// TODO: implement
								} else {
									// If we don't have to wait for the parent to be healhy, let's restart the children now
									if err := cli.ContainerRestart(ctx, container.ID, containerType.StopOptions{}); err != nil {
										// TODO: handle error
									}
									fmt.Printf("Restarting container: %s, depends on: %s which just started\n", childName, parentName)
								}
							}
						}
					}
				}
				if _, ok := watchedContainers[event.Actor.ID]; ok {
					if event.Action == "stop" || event.Action == "die" {
						name := strings.TrimPrefix(watchedContainers[event.Actor.ID].Names[0], "/")
						fmt.Printf("Container stopped: %s\n", name)
						delete(watchedContainers, event.Actor.ID)
					}

					if event.Action == "health_status" {
						// TODO: implement
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
