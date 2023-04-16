package main

import (
	"context"
	"fmt"
	"strconv"
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
						// fmt.Println(event.Type)
						// fmt.Println(event.Action)
						// fmt.Printf("%+v\n", event.Actor)
						// fmt.Println()
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

						// check if the containers that we are watching depend on this container
						// if they do, somehow schedule a restart on them
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

								// fmt.Printf("CONTAINER: %+v\n", container)
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
				// if event.Type == "container" && event.Action == "health_status" || event.Action == "start" {
				// 	// Check if the container is one of our dependant containers
				// 	if contains(watchedContainers, event.Actor.ID) {
				// 		// Find all containers that depend on this container
				// 		dependentContainers := []string{}
				// 		for _, depContainer := range containers {
				// 			if hasLabel(depContainer.Labels, "dependheal.depends_on", event.Actor.Attributes["name"]) {
				// 				dependentContainers = append(dependentContainers, depContainer.ID)
				// 			}
				// 		}

				// 		// Restart dependent containers with delay
				// 		for _, depContainer := range dependentContainers {
				// 			depContainerInfo, err := cli.ContainerInspect(ctx, depContainer)
				// 			if err != nil {
				// 				panic(err)
				// 			}

				// 			waitForParent := hasLabel(depContainerInfo.Config.Labels, "dependheal.restart.wait_for_parent", "true")
				// 			if waitForParent {
				// 				fmt.Printf("Waiting for container %s to be healthy...\n", event.Actor.Attributes["name"])
				// 				for {
				// 					containerInfo, err := cli.ContainerInspect(ctx, event.Actor.ID)
				// 					if err != nil {
				// 						panic(err)
				// 					}

				// 					if containerInfo.State.Status == "running" {
				// 						break
				// 					}

				// 					time.Sleep(time.Second)
				// 				}

				// 				fmt.Printf("Restarting dependent container %s immediately...\n", depContainerInfo.Name)
				// 				err = cli.ContainerRestart(ctx, depContainer, container.StopOptions{})
				// 				if err != nil {
				// 					panic(err)
				// 				}
				// 			} else {
				// 				delay := getLabelInt(depContainerInfo.Config.Labels, "dependheal.restart.delay", 0)
				// 				if delay > 0 {
				// 					fmt.Printf("Delaying restart of container %s for %d seconds...\n", depContainerInfo.Name, delay)
				// 					time.Sleep(time.Duration(delay) * time.Second)
				// 				}

				// 				fmt.Printf("Restarting dependent container %s...\n", depContainerInfo.Name)
				// 				err = cli.ContainerRestart(ctx, depContainer, container.StopOptions{})
				// 				if err != nil {
				// 					panic(err)
				// 				}
				// 			}
				// 		}
				// 	}
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

func getLabelInt(labels map[string]string, key string, defaultValue int) int {
	valueStr, ok := labels[key]
	if !ok {
		return defaultValue
	}

	value, err := strconv.Atoi(valueStr)
	if err != nil {
		panic(fmt.Errorf("Failed to parse label '%s' value '%s' as integer: %w", key, valueStr, err))
	}

	return value
}

func contains(slice []string, element string) bool {
	for _, item := range slice {
		if item == element {
			return true
		}
	}
	return false
}
