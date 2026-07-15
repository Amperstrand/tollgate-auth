package main

import (
	"fmt"
	"os/exec"
)

const (
	bridgeName = "br-fc"
	subnet     = "172.16.0.0/24"
	gatewayIP  = "172.16.0.1"
)

func ensureNAT() error {
	exec.Command("sh", "-c", "echo 1 > /proc/sys/net/ipv4/ip_forward").Run()

	if err := exec.Command("sh", "-c",
		fmt.Sprintf("ip link show %s 2>/dev/null || (ip link add %s type bridge && ip addr add %s/24 dev %s && ip link set %s up)",
			bridgeName, bridgeName, gatewayIP, bridgeName, bridgeName)).Run(); err != nil {
		return fmt.Errorf("bridge setup: %w", err)
	}

	exec.Command("sh", "-c",
		fmt.Sprintf("iptables -t nat -C POSTROUTING -s %s -o eth0 -j MASQUERADE 2>/dev/null || iptables -t nat -A POSTROUTING -s %s -o eth0 -j MASQUERADE", subnet, subnet)).Run()

	exec.Command("sh", "-c",
		"iptables -C FORWARD -i br-fc -o eth0 -j ACCEPT 2>/dev/null || iptables -A FORWARD -i br-fc -o eth0 -j ACCEPT").Run()

	exec.Command("sh", "-c",
		"iptables -C FORWARD -i eth0 -o br-fc -m state --state RELATED,ESTABLISHED -j ACCEPT 2>/dev/null || iptables -A FORWARD -i eth0 -o br-fc -m state --state RELATED,ESTABLISHED -j ACCEPT").Run()

	return nil
}

func setupTAP(tapName string) error {
	if err := exec.Command("ip", "tuntap", "add", tapName, "mode", "tap").Run(); err != nil {
		return fmt.Errorf("tuntap add: %w", err)
	}
	if err := exec.Command("ip", "link", "set", tapName, "up").Run(); err != nil {
		cleanupTAP(tapName)
		return fmt.Errorf("link up: %w", err)
	}
	if err := exec.Command("ip", "link", "set", tapName, "master", bridgeName).Run(); err != nil {
		cleanupTAP(tapName)
		return fmt.Errorf("bridge attach: %w", err)
	}
	return nil
}

func cleanupTAP(tapName string) {
	exec.Command("ip", "link", "delete", tapName).Run()
}
