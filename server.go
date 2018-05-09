// Copyright (c) 2017, NVIDIA CORPORATION. All rights reserved.

package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/net/context"
	"google.golang.org/grpc"

	pluginapi "k8s.io/kubernetes/pkg/kubelet/apis/deviceplugin/v1alpha"
	pluginregistration "k8s.io/kubernetes/pkg/kubelet/apis/pluginregistration/v1beta"
)

const (
	resourceName = "nvidia.com/gpu"
	serverSock   = pluginapi.DevicePluginsPath + "/nvidia.com/gpu.sock"
)

type InitResponse = pluginapi.InitContainerResponse
type InitRequest = pluginapi.InitContainerRequest

type AdmitRequest = pluginapi.AdmitPodRequest

type VersionsRequest = pluginregistration.GetSupportedVersionsRequest
type VersionsResponse = pluginregistration.GetSupportedVersionsResponse

type IdentityRequest = pluginregistration.GetPluginIdentityRequest
type IdentityResponse = pluginregistration.GetPluginIdentityResponse

type InfoRequest = pluginapi.GetPluginInfoRequest
type InfoResponse = pluginapi.GetPluginInfoResponse

type ListAndWatchStream = pluginapi.DevicePlugin_ListAndWatchServer

// NvidiaDevicePlugin implements the Kubernetes device plugin API
type NvidiaDevicePlugin struct {
	devs   []*pluginapi.Device
	socket string

	stop   chan interface{}
	health chan *pluginapi.Device

	server *grpc.Server
}

// NewNvidiaDevicePlugin returns an initialized NvidiaDevicePlugin
func NewNvidiaDevicePlugin() *NvidiaDevicePlugin {
	return &NvidiaDevicePlugin{
		devs:   getDevices(),
		socket: serverSock,

		stop:   make(chan interface{}),
		health: make(chan *pluginapi.Device),
	}
}

// Start starts the gRPC server of the device plugin
func (m *NvidiaDevicePlugin) Start() error {
	err := m.cleanup()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(m.socket), 0755); err != nil {
		return fmt.Errorf("Failed to create Device Plugin dir: %+v", err)
	}

	sock, err := net.Listen("unix", m.socket)
	if err != nil {
		return err
	}

	m.server = grpc.NewServer([]grpc.ServerOption{}...)
	pluginapi.RegisterDevicePluginServer(m.server, m)
	pluginregistration.RegisterIdentityServer(m.server, m)

	go m.server.Serve(sock)
	go m.healthcheck()

	log.Println("Starting to serve on", m.socket)
	return nil
}

// Stop stops the gRPC server
func (m *NvidiaDevicePlugin) Stop() error {
	if m.server == nil {
		return nil
	}

	m.server.Stop()
	m.server = nil
	close(m.stop)

	return m.cleanup()
}

func (m *NvidiaDevicePlugin) GetSupportedVersions(ctx context.Context, in *VersionsRequest) (*VersionsResponse, error) {
	return &VersionsResponse{
		SupportedVersions: []string{pluginapi.Version},
	}, nil
}

func (m *NvidiaDevicePlugin) GetPluginIdentity(ctx context.Context, in *IdentityRequest) (*IdentityResponse, error) {
	return &IdentityResponse{
		ResourceName: resourceName,
	}, nil
}

func (m *NvidiaDevicePlugin) GetPluginInfo(ctx context.Context, in *InfoRequest) (*InfoResponse, error) {
	return &InfoResponse{
		InitTimeout: 1,
		Labels:      map[string]string{},
	}, nil
}

func (m *NvidiaDevicePlugin) PluginRegistrationStatus(ctx context.Context, in *pluginregistration.RegistrationStatus) (*pluginregistration.Empty, error) {
	log.Printf("PluginRegistrationStatus: %v", in)
	return &pluginregistration.Empty{}, nil
}

// ListAndWatch lists devices and update that list according to the health status
func (m *NvidiaDevicePlugin) ListAndWatch(e *pluginapi.ListAndWatchRequest, s ListAndWatchStream) error {
	s.Send(&pluginapi.ListAndWatchResponse{Devices: m.devs})

	for {
		select {
		case <-m.stop:
			return nil
		case d := <-m.health:
			// FIXME: there is no way to recover from the Unhealthy state.
			d.Health = pluginapi.Unhealthy
			s.Send(&pluginapi.ListAndWatchResponse{Devices: m.devs})
		}
	}
}

// InitializeContainer is called at container initialization
func (m *NvidiaDevicePlugin) InitContainer(ctx context.Context, in *InitRequest) (*InitResponse, error) {
	log.Printf("InitContainer: %v", in)
	devs := m.devs

	response := InitResponse{
		Spec: &pluginapi.ContainerSpec{
			Envs: map[string]string{
				"NVIDIA_VISIBLE_DEVICES": strings.Join(in.Container.Devices, ","),
			},
			Annotations: map[string]string{
				"annotation.io.kubernetes.container.runtime": "nvidia",
			},
		},
	}

	for _, id := range in.Container.Devices {
		if !deviceExists(devs, id) {
			return nil, fmt.Errorf("invalid allocation request: unknown device: %s", id)
		}
	}

	return &response, nil
}

func (m *NvidiaDevicePlugin) AdmitPod(ctx context.Context, in *pluginapi.AdmitPodRequest) (*pluginapi.AdmitPodResponse, error) {
	log.Printf("AdmitPod: %v", in)

	// This is required because CRIO 1.9 will incorrectly loop over pod annotations instead
	// of pod annotations
	return &pluginapi.AdmitPodResponse{
		Pod: &pluginapi.PodSpec{
			Annotations: map[string]string{
				"annotation.io.kubernetes.container.runtime": "nvidia",
			},
		},
	}, nil
}

func (m *NvidiaDevicePlugin) unhealthy(dev *pluginapi.Device) {
	m.health <- dev
}

func (m *NvidiaDevicePlugin) cleanup() error {
	if err := os.Remove(m.socket); err != nil && !os.IsNotExist(err) {
		return err
	}

	return nil
}

func (m *NvidiaDevicePlugin) healthcheck() {
	ctx, cancel := context.WithCancel(context.Background())

	xids := make(chan *pluginapi.Device)
	go watchXIDs(ctx, m.devs, xids)

	for {
		select {
		case <-m.stop:
			cancel()
			return
		case dev := <-xids:
			m.unhealthy(dev)
		}
	}
}
