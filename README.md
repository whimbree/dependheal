# Dependheal

A docker container autorestart tool.

## Motivation
When using docker-compose with the `network_mode: "service:parent_name"` option or docker with the `--net:container:parent_name`, the attached container will permanantly lose its network connection if the parent container restarts. 

This mode enables using the network stack of another container (eg: for tunneling traffic through a VPN), but this inability to restart children makes it less useful in scenarios where the parent container sometimes restarts.

There have been issues raised about this for many years on the Docker github, but they were not resolved:

- https://github.com/docker/compose/issues/6329
- https://github.com/docker/compose/issues/6626

## Setup
Dependheal will listen to the docker daemon to know when a container starts or stops.
Containers must have the `dependheal.enable=true` label for Dependheal to listen for them.
Alternatively, Dependheal will listen to all containers if the environment variable `DEPENDHEAL_ENABLE_ALL=true`.

Dependheal will connect containers to networks if the `dependheal.networks` label exists on the container.
This label accepts a comma delimited list of networks, i.e. `dependheal.networks = network1, network2`.

Any container with the `dependheal.parent=<parent_name>` label will be restarted automatically if the `<parent_name>` container restarts.

If the `dependheal.wait_for_parent_healthy=true` label is set, the container will be restarted once the parent container's healthcheck passes. Otherwise Dependheal will immediately restart it.

Dependheal will also automatically restart containers that have failing healthchecks. To add a timeout between a container going unhealthy and Dependheal restarting it, add this label `dependheal.timeout=<timeout>` where `<timeout>` is either an integer or a floating point number. Otherwise Dependheal will immediately restart it.

By default, dependheal will log messages at the info level. To get debug logging, pass the environment variable `DEBUG=true`.

## Usage
Run `go mod download` to download dependencies.

Run `go build` to build the `dependheal` executable.

Run the executable with `./dependheal`

## Usage in Docker
Run `docker build -t dependheal .` to build the docker image.

Run `docker run -d --name dependheal --restart=unless-stopped -v /var/run/docker.sock:/var/run/docker.sock dependheal:latest` to start the docker image.

## Sample usage with docker-compose

```
version: "3.9"
services:
  dependheal:
    container_name: dependheal
    image: dependheal:latest
    restart: unless-stopped
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock

  parent_container:
    container_name: parent_container
    image: parent:latest
    restart: unless-stopped
    labels:
      - "dependheal.enable=true"

  child_container:
    container_name: child_container
    image: child:latest
    depends_on:
      - parent_container
    network_mode: service:parent_container
    labels:
      - "dependheal.enable=true"
      - "dependheal.parent=parent_container"
```