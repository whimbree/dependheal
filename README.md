# Dependheal

## Motivation
When using docker-compose with the `network_mode: "service:parent_name"` option, or docker with the `--net:container:parent_name` to use the network stack of another container (eg: for tunneling traffic through a VPN), the attached container will permanantly lose its network connection if the parent container restarts.

There have been issues raised about this for many years on the Docker github, but they were not resolved:

- https://github.com/docker/compose/issues/6329
- https://github.com/docker/compose/issues/6626

## Setup
Dependheal will listen to the docker daemon to know when a container starts or stops.
Containers must have the `dependheal.enable=true` label for dependheal to listen for them.

Any container with the `dependheal.parent=parent_name` label will be restarted automatically if the `parent_name` container restarts.

## Usage
Run `go mod download` to download dependencies.
Run `go build` to build the `dependheal` executable.
Run the executable with `./dependheal`

## Usage in Docker
Run `docker build -t dependheal_img .` to build the docker image.
Run `docker run -d --name dependheal --restart=unless-stopped -v /var/run/docker.sock:/var/run/docker.sock dependheal_img` to start the docker image.


## Sample usage with docker-compose

```
version: "3.9"
services:
  dependheal:
    container_name: dependheal
    image: dependheal_img
    restart: unless-stopped
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock

  parent_container:
    container_name: parent_container
    image: parent_container_img:latest
    restart: unless-stopped
    labels:
      - "dependheal.enable=true"

  child_container:
    container_name: child_container
    image: child_container_img:latest
    depends_on:
      - parent_container
    network_mode: service:parent_container
    labels:
      - "dependheal.enable=true"
      - "dependheal.parent=parent_container"
```