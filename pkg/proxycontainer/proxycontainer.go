package proxycontainer

import (
	"bytes"
	"context"
	"fmt"
	"os"

	"github.com/craftcms/nitro/command/version"
	"github.com/craftcms/nitro/pkg/labels"
	"github.com/craftcms/nitro/pkg/terminal"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

var (
	ProxyImage = fmt.Sprintf("craftcms/nitro-proxy:%s", version.Version)

	// ErrNoProxyContainer is returned when the proxy container is not found
	ErrNoProxyContainer = fmt.Errorf("unable to locate the proxy container")
)

func Create(ctx context.Context, docker client.CommonAPIClient, output terminal.Outputer, volume *types.Volume, networkID string) error {
	// TODO(jasonmccallister) remove this after development
	if os.Getenv("NITRO_DEVELOPMENT") != "true" {
		imageFilter := filters.NewArgs()
		imageFilter.Add("label", labels.Nitro+"=true")
		imageFilter.Add("reference", ProxyImage)

		// check for the proxy image
		images, err := docker.ImageList(ctx, types.ImageListOptions{
			Filters: imageFilter,
		})
		if err != nil {
			return fmt.Errorf("unable to get a list of images, %w", err)
		}

		// if there are no local images, pull it
		if len(images) == 0 {
			output.Pending("pulling image")

			rdr, err := docker.ImagePull(ctx, ProxyImage, types.ImagePullOptions{All: false})
			if err != nil {
				return fmt.Errorf("unable to pull the nitro-proxy from docker hub, %w", err)
			}

			buf := &bytes.Buffer{}
			if _, err := buf.ReadFrom(rdr); err != nil {
				return fmt.Errorf("unable to read the output from pulling the image, %w", err)
			}

			output.Done()
		}
	}

	// create a filter for the nitro proxy
	filter := filters.NewArgs()
	filter.Add("label", labels.Nitro+"=true")
	filter.Add("label", labels.Proxy+"=true")

	// check if there is an existing container for the nitro-proxy
	var containerID string
	containers, err := docker.ContainerList(ctx, types.ContainerListOptions{Filters: filter, All: true})
	if err != nil {
		return fmt.Errorf("unable to list the containers\n%w", err)
	}

	var proxyRunning bool
	for _, c := range containers {
		for _, n := range c.Names {
			if n == "nitro-proxy" || n == "/nitro-proxy" {
				output.Success("proxy ready")

				containerID = c.ID

				// check if it is running
				if c.State == "running" {
					proxyRunning = true
				}
			}
		}
	}

	// if we do not have a container id, it needs to be create
	if containerID == "" {
		output.Pending("creating proxy")

		// set ports
		var httpPort, httpsPort, apiPort nat.Port

		// check for a custom HTTP port
		switch os.Getenv("NITRO_HTTP_PORT") {
		case "":
			httpPort, err = nat.NewPort("tcp", "80")
			if err != nil {
				return fmt.Errorf("unable to set the HTTP port, %w", err)
			}
		default:
			httpPort, err = nat.NewPort("tcp", os.Getenv("NITRO_HTTP_PORT"))
			if err != nil {
				return fmt.Errorf("unable to set the HTTP port, %w", err)
			}
		}

		// check for a custom HTTPS port
		switch os.Getenv("NITRO_HTTPS_PORT") {
		case "":
			httpsPort, err = nat.NewPort("tcp", "443")
			if err != nil {
				return fmt.Errorf("unable to set the HTTPS port, %w", err)
			}
		default:
			httpsPort, _ = nat.NewPort("tcp", os.Getenv("NITRO_HTTPS_PORT"))
			if err != nil {
				return fmt.Errorf("unable to set the HTTPS port, %w", err)
			}
		}

		// check for a custom API port
		switch os.Getenv("NITRO_API_PORT") {
		case "":
			apiPort, err = nat.NewPort("tcp", "5000")
			if err != nil {
				return fmt.Errorf("unable to set the API port, %w", err)
			}
		default:
			apiPort, err = nat.NewPort("tcp", os.Getenv("NITRO_API_PORT"))
			if err != nil {
				return fmt.Errorf("unable to set the API port, %w", err)
			}
		}

		// create a container
		resp, err := docker.ContainerCreate(ctx,
			&container.Config{
				Image: ProxyImage,
				ExposedPorts: nat.PortSet{
					httpPort:  struct{}{},
					httpsPort: struct{}{},
					apiPort:   struct{}{},
				},
				Labels: map[string]string{
					labels.Nitro:        "true",
					labels.Type:         "proxy",
					labels.Proxy:        "true",
					labels.ProxyVersion: version.Version,
				},
			},
			&container.HostConfig{
				NetworkMode: "default",
				Mounts: []mount.Mount{
					{
						Type:   mount.TypeVolume,
						Source: volume.Name,
						Target: "/data",
					},
				},
				PortBindings: map[nat.Port][]nat.PortBinding{
					httpPort: {
						{
							HostIP:   "127.0.0.1",
							HostPort: "80",
						},
					},
					httpsPort: {
						{
							HostIP:   "127.0.0.1",
							HostPort: "443",
						},
					},
					apiPort: {
						{
							HostIP:   "127.0.0.1",
							HostPort: "5000",
						},
					},
				},
			},
			&network.NetworkingConfig{
				EndpointsConfig: map[string]*network.EndpointSettings{
					"nitro-network": {
						NetworkID: networkID,
					},
				},
			},
			nil,
			"nitro-proxy",
		)
		if err != nil {
			return fmt.Errorf("unable to create the container from image %s\n%w", ProxyImage, err)
		}

		containerID = resp.ID

		output.Done()
	}

	// start the container for the proxy if its not running
	if !proxyRunning {
		if err := docker.ContainerStart(ctx, containerID, types.ContainerStartOptions{}); err != nil {
			return fmt.Errorf("unable to start the nitro container, %w", err)
		}
	}

	return nil
}

// FindAndStart will look for the proxy container and verify the container is started. It will return the
// ErrNoProxyContainer error if it is unable to locate the proxy container. It is NOT responsible for
// creating the proxy container as that is handled in the initialize package.
func FindAndStart(ctx context.Context, docker client.ContainerAPIClient) (types.Container, error) {
	// create the filters for the proxy
	f := filters.NewArgs()
	f.Add("label", labels.Type+"=proxy")

	// check if there is an existing container for the nitro-proxy
	containers, err := docker.ContainerList(ctx, types.ContainerListOptions{Filters: f, All: true})
	if err != nil {
		return types.Container{}, fmt.Errorf("unable to list the containers: %w", err)
	}

	for _, c := range containers {
		for _, n := range c.Names {
			if n == "nitro-proxy" || n == "/nitro-proxy" {
				// check if it is running
				if c.State != "running" {
					if err := docker.ContainerStart(ctx, c.ID, types.ContainerStartOptions{}); err != nil {
						return types.Container{}, fmt.Errorf("unable to start the proxy container: %w", err)
					}
				}

				// return the container
				return c, nil
			}
		}
	}

	return types.Container{}, ErrNoProxyContainer
}