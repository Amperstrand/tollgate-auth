package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	defaultFirecracker = "/usr/local/bin/firecracker"
	defaultVmlinux     = "/tmp/vmlinux"
	defaultInitramfs   = "/tmp/initramfs.cpio.gz"
	defaultVMBase      = "/tmp/fc-vms"
	defaultMem         = 256
	defaultVCPUs       = 1
)

var (
	bindAddr  = getenv("FC_BIND", "0.0.0.0")
	bindPort  = getenv("FC_PORT", "8081")
	apiKey    = os.Getenv("FC_API_KEY")
	fcBinary  = getenv("FIRECRACKER", defaultFirecracker)
	vmlinux   = getenv("VMLINUX", defaultVmlinux)
	initramfs = getenv("INITRAMFS", defaultInitramfs)
	vmBase    = getenv("VM_BASE", defaultVMBase)
)

var (
	vms      = make(map[string]*VM)
	vmsMutex sync.Mutex
)

type VM struct {
	ID        string `json:"id"`
	IP        string `json:"ip"`
	Rootfs    string `json:"rootfs"`
	CPUs      int    `json:"cpus"`
	MemMB     int    `json:"mem_mb"`
	proc      *os.Process
	tap       string
	logFile   *os.File
	ttl       int
	expiresAt time.Time
}

type createRequest struct {
	CPUs       int    `json:"cpus"`
	MemMB      int    `json:"mem_mb"`
	Rootfs     string `json:"rootfs"`
	TTLSeconds int    `json:"ttl_seconds"`
}

func main() {
	os.MkdirAll(vmBase, 0755)
	ensureNAT()
	startReaper()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sig
		destroyAllVMs()
		os.Exit(0)
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/vms", handleVMs)
	mux.HandleFunc("/vms/", handleVMByID)

	addr := fmt.Sprintf("%s:%s", bindAddr, bindPort)
	rootfsList := detectRootfs()
	log.Printf("[tollgate-fcd] listening on %s (%d VMs, rootfs=%v)", addr, len(vms), rootfsList)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"status": "ok",
		"vms":    len(vms),
		"rootfs": detectRootfs(),
	})
}

func handleVMs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		if !checkAuth(r) {
			writeJSON(w, 401, map[string]string{"error": "Unauthorized"})
			return
		}
		vmsMutex.Lock()
		defer vmsMutex.Unlock()
		list := make([]map[string]any, 0, len(vms))
		for id, vm := range vms {
			list = append(list, map[string]any{
				"id":     id,
				"alive":  vm.proc != nil && vm.signal(syscall.Signal(0)) == nil,
				"rootfs": vm.Rootfs,
			})
		}
		writeJSON(w, 200, list)

	case "POST":
		if !checkAuth(r) {
			writeJSON(w, 401, map[string]string{"error": "Unauthorized"})
			return
		}
		var req createRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, 400, map[string]string{"error": "invalid body"})
			return
		}
		if req.CPUs == 0 {
			req.CPUs = defaultVCPUs
		}
		if req.MemMB == 0 {
			req.MemMB = defaultMem
		}
		if req.Rootfs == "" {
			req.Rootfs = "initramfs"
		}

		vm, err := createVM(req)
		if err != nil {
			log.Printf("[tollgate-fcd] VM creation failed: %v", err)
			writeJSON(w, 500, map[string]string{"error": fmt.Sprintf("VM creation failed: %v", err)})
			return
		}

		writeJSON(w, 201, vm)

	default:
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
	}
}

func handleVMByID(w http.ResponseWriter, r *http.Request) {
	if !checkAuth(r) {
		writeJSON(w, 401, map[string]string{"error": "Unauthorized"})
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/vms/")
	if id == "" {
		writeJSON(w, 400, map[string]string{"error": "missing VM ID"})
		return
	}

	switch r.Method {
	case "DELETE":
		if err := destroyVM(id); err != nil {
			writeJSON(w, 404, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]string{"status": "destroyed", "id": id})
	default:
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
	}
}

func checkAuth(r *http.Request) bool {
	if apiKey == "" {
		return true
	}
	return r.Header.Get("Authorization") == "Bearer "+apiKey
}

func writeJSON(w http.ResponseWriter, code int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(data)
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func detectRootfs() []string {
	var r []string
	if fileExists(vmlinux) && fileExists(initramfs) {
		r = append(r, "initramfs")
	}
	return r
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (vm *VM) signal(sig os.Signal) error {
	return vm.proc.Signal(sig)
}

func destroyAllVMs() {
	vmsMutex.Lock()
	defer vmsMutex.Unlock()
	for id, vm := range vms {
		vm.proc.Kill()
		if vm.logFile != nil {
			vm.logFile.Close()
		}
		runCmd("ip", "link", "delete", vm.tap)
		delete(vms, id)
	}
}
