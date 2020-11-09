package client

import (
	"context"
	"reflect"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	volumetypes "github.com/docker/docker/api/types/volume"
	"github.com/docker/go-connections/nat"
)

func TestInitFromFreshCreatesNewResources(t *testing.T) {
	// Arrange
	environmentName := "testing-init"
	mock := newMockDockerClient(nil, nil, nil)
	mock.networkCreateResponse = types.NetworkCreateResponse{
		ID: "testing-init",
	}
	mock.containerCreateResponse = container.ContainerCreateCreatedBody{
		ID: "testingid",
	}
	cli := Client{docker: mock}

	// Expected
	// set the network create request
	networkReq := types.NetworkCreateRequest{
		NetworkCreate: types.NetworkCreate{
			Driver:     "bridge",
			Attachable: true,
			Labels: map[string]string{
				"com.craftcms.nitro.network": "testing-init",
			},
		},
		Name: "testing-init",
	}
	// set the volume create request
	volumeReq := volumetypes.VolumesCreateBody{
		Driver: "local",
		Name:   "testing-init",
		Labels: map[string]string{
			"com.craftcms.nitro.volume": "testing-init",
		},
	}
	// set the container create request
	containerCreateReq := types.ContainerCreateConfig{
		// TODO(jasonmccallister) get this as a param or CLI version
		Config: &container.Config{
			Image: "testing-caddy:latest",
			ExposedPorts: nat.PortSet{
				"80/tcp":   struct{}{},
				"443/tcp":  struct{}{},
				"5000/tcp": struct{}{},
			},
			Labels: map[string]string{
				"com.craftcms.nitro.proxy": "testing-init",
			},
		},
		HostConfig: &container.HostConfig{
			NetworkMode: "default",
			PortBindings: map[nat.Port][]nat.PortBinding{
				"80/tcp": {
					{
						HostIP:   "127.0.0.1",
						HostPort: "80",
					},
				},
				"443/tcp": {
					{
						HostIP:   "127.0.0.1",
						HostPort: "443",
					},
				},
				"5000/tcp": {
					{
						HostIP:   "127.0.0.1",
						HostPort: "5000",
					},
				},
			},
		},
		NetworkingConfig: &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				"testing-init": {
					NetworkID: "testing-init",
				},
			},
		},
		Name: "nitro-proxy",
	}
	// set the container start request
	containerStartRequest := types.ContainerStartOptions{}

	// Act
	err := cli.Init(context.TODO(), environmentName, []string{})

	// Assert
	if err != nil {
		t.Errorf("expected the error to be nil, got %v", err)
	}

	// make sure the network create matches the expected
	if !reflect.DeepEqual(mock.networkCreateRequest, networkReq) {
		t.Errorf(
			"expected network create request to match\ngot:\n%v\nwant:\n%v",
			mock.networkCreateRequest,
			networkReq,
		)
	}

	// make sure the volume create matches the expected
	if !reflect.DeepEqual(mock.volumeCreateRequest, volumeReq) {
		t.Errorf(
			"expected volume create request to match\ngot:\n%v\nwant:\n%v",
			mock.volumeCreateRequest,
			volumeReq,
		)
	}

	// make sure the container create matches the expected
	if !reflect.DeepEqual(mock.containerCreateRequest, containerCreateReq) {
		t.Errorf(
			"expected container create request to match\ngot:\n%v\nwant:\n%v",
			mock.containerCreateRequest,
			containerCreateReq,
		)
	}

	// make sure the container start matches the expected
	if !reflect.DeepEqual(mock.containerStartRequest, containerStartRequest) {
		t.Errorf(
			"expected container start request to match\ngot:\n%v\nwant:\n%v",
			mock.containerStartRequest,
			containerStartRequest,
		)
	}

	// make sure the container ID to start matches the expected
	if mock.containerID != "testingid" {
		t.Errorf(
			"expected container IDs to start to match\ngot:\n%v\nwant:\n%v",
			mock.containerID,
			"testingid",
		)
	}
}