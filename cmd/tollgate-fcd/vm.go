package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func createVM(req createRequest) (*VM, error) {
	vmsMutex.Lock()
	defer vmsMutex.Unlock()

	id := generateID()
	vmDir := filepath.Join(vmBase, id)
	if err := os.MkdirAll(vmDir, 0755); err != nil {
		return nil, fmt.Errorf("mkdir: %w", err)
	}

	ip := nextVMIP()
	tapName := "fc" + id[:8]

	if err := setupTAP(tapName); err != nil {
		os.RemoveAll(vmDir)
		return nil, fmt.Errorf("tap setup: %w", err)
	}

	configPath := filepath.Join(vmDir, "config.json")
	serialPath := filepath.Join(vmDir, "serial.log")
	vsockPath := filepath.Join(vmDir, "v.sock")

	config := map[string]any{
		"boot-source": map[string]any{
			"kernel_image_path": vmlinux,
			"initrd_path":       initramfs,
			"boot_args":         fmt.Sprintf("console=ttyS0 reboot=k panic=1 fcip=%s", ip),
		},
		"drives": []any{},
		"machine-config": map[string]any{
			"vcpu_count":   req.CPUs,
			"mem_size_mib": req.MemMB,
		},
		"vsock": map[string]any{
			"guest_cid": nextCID(),
			"uds_path":  vsockPath,
		},
		"network-interfaces": []map[string]any{
			{
				"iface_id":      "net0",
				"host_dev_name": tapName,
			},
		},
	}

	configData, _ := json.MarshalIndent(config, "", "  ")
	if err := os.WriteFile(configPath, configData, 0644); err != nil {
		cleanupTAP(tapName)
		os.RemoveAll(vmDir)
		return nil, fmt.Errorf("write config: %w", err)
	}

	serialFile, err := os.Create(serialPath)
	if err != nil {
		cleanupTAP(tapName)
		os.RemoveAll(vmDir)
		return nil, fmt.Errorf("create serial log: %w", err)
	}

	cmd := exec.Command(fcBinary, "--no-api", "--config-file", configPath)
	cmd.Stdout = serialFile
	cmd.Stderr = serialFile

	if err := cmd.Start(); err != nil {
		serialFile.Close()
		cleanupTAP(tapName)
		os.RemoveAll(vmDir)
		return nil, fmt.Errorf("start firecracker: %w", err)
	}

	vm := &VM{
		ID:        id,
		IP:        ip,
		Rootfs:    req.Rootfs,
		CPUs:      req.CPUs,
		MemMB:     req.MemMB,
		proc:      cmd.Process,
		tap:       tapName,
		logFile:   serialFile,
		ttl:       req.TTLSeconds,
		expiresAt: time.Now().Add(time.Duration(req.TTLSeconds) * time.Second),
	}
	vms[id] = vm

	go func() {
		cmd.Wait()
		vmsMutex.Lock()
		if v, ok := vms[id]; ok {
			if v.logFile != nil {
				v.logFile.Close()
			}
			cleanupTAP(v.tap)
			delete(vms, id)
		}
		vmsMutex.Unlock()
		log.Printf("[tollgate-fcd] VM %s exited", id)
	}()

	if vm.ttl > 0 {
		go func() {
			time.Sleep(time.Duration(vm.ttl) * time.Second)
			log.Printf("[tollgate-fcd] VM %s TTL expired (%ds), destroying", id, vm.ttl)
			destroyVM(id)
		}()
	}

	log.Printf("[tollgate-fcd] VM %s created (ip=%s, rootfs=%s, tap=%s)", id, ip, req.Rootfs, tapName)

	return &VM{
		ID:     id,
		IP:     ip,
		Rootfs: req.Rootfs,
		CPUs:   req.CPUs,
		MemMB:  req.MemMB,
	}, nil
}

func startReaper() {
	go func() {
		for {
			time.Sleep(30 * time.Second)
			vmsMutex.Lock()
			now := time.Now()
			for id, vm := range vms {
				if vm.ttl > 0 && now.After(vm.expiresAt) {
					log.Printf("[tollgate-fcd] Reaper: destroying expired VM %s", id)
					vm.proc.Kill()
					if vm.logFile != nil {
						vm.logFile.Close()
					}
					cleanupTAP(vm.tap)
					os.RemoveAll(filepath.Join(vmBase, id))
					delete(vms, id)
				}
			}
			vmsMutex.Unlock()
		}
	}()
}

func destroyVM(id string) error {
	vmsMutex.Lock()
	defer vmsMutex.Unlock()

	vm, ok := vms[id]
	if !ok {
		return fmt.Errorf("VM %s not found", id)
	}

	vm.proc.Kill()
	if vm.logFile != nil {
		vm.logFile.Close()
	}
	cleanupTAP(vm.tap)

	vmDir := filepath.Join(vmBase, id)
	os.RemoveAll(vmDir)

	delete(vms, id)
	log.Printf("[tollgate-fcd] VM %s destroyed", id)
	return nil
}

var (
	ipCounter  = 1
	cidCounter = 2
)

func nextVMIP() string {
	ip := fmt.Sprintf("172.16.0.%d", ipCounter+1)
	ipCounter++
	if ipCounter > 250 {
		ipCounter = 1
	}
	return ip
}

func nextCID() int {
	cidCounter++
	if cidCounter > 250 {
		cidCounter = 2
	}
	return cidCounter
}

func generateID() string {
	b := make([]byte, 6)
	if _, err := readRandom(b); err != nil {
		return fmt.Sprintf("%012x", os.Getpid())
	}
	return fmt.Sprintf("%012x", b)
}

func readRandom(b []byte) (int, error) {
	f, err := os.Open("/dev/urandom")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return f.Read(b)
}

func runCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}

func runCmdOutput(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
