// Copyright (c) 2017, NVIDIA CORPORATION. All rights reserved.

package main

import (
	"log"
	"syscall"

	"github.com/NVIDIA/nvidia-docker/src/nvml"
)

func main() {
	log.Println("Loading NVML")
	if err := nvml.Init(); err != nil {
		log.Printf("Failed to start nvml with error: %s.", err)
		log.Printf("If this is a GPU node, did you set the docker default runtime to `nvidia`?")
		log.Printf("You can check the prerequisites at: https://github.com/NVIDIA/k8s-device-plugin#prerequisites")
		log.Printf("You can learn how to set the runtime at: https://github.com/NVIDIA/k8s-device-plugin#quick-start")

		select {}
	}
	defer func() { log.Println("Shutdown of NVML returned:", nvml.Shutdown()) }()

	log.Println("Fetching devices.")
	if len(getDevices()) == 0 {
		log.Println("No devices found. Waiting indefinitely.")
		select {}
	}

	log.Println("Starting OS watcher.")
	sigs := newOSWatcher(syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)

	var devicePlugin *NvidiaDevicePlugin

	devicePlugin = NewNvidiaDevicePlugin()
	if err := devicePlugin.Start(); err != nil {
		log.Println("Failed to start Device Plugin with error %+v.", err)
		select {}
	}

	// TODO kill if socket gets removed
	s := <-sigs
	log.Printf("Received signal \"%v\", shutting down.", s)
	devicePlugin.Stop()
}
