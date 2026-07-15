{ config, pkgs, lib, ... }:
{
  imports = [ ./hardware-configuration.nix ];

  networking.hostName = "fc-host";
  networking.interfaces.eth0.useDHCP = true;
  networking.firewall.enable = false;

  boot.kernel.sysctl."net.ipv4.ip_forward" = 1;
  boot.kernelModules = [ "kvm-intel" "kvm-amd" "vhost_vsock" ];

  services.openssh = {
    enable = true;
    settings.PermitRootLogin = "prohibit-password";
  };

  nix.settings.experimental-features = [ "nix-command" "flakes" ];

  environment.systemPackages = with pkgs; [
    firecracker
    python3
    busybox
    curl
    git
    bridge-utils
    iptables
  ];

  systemd.services.fc-daemon = {
    description = "Firecracker microVM daemon";
    after = [ "network.target" ];
    wantedBy = [ "multi-user.target" ];
    environment = {
      FC_ROOTFS_DIR = "/var/lib/firecracker/rootfs";
      FC_VMS_DIR = "/var/lib/firecracker/vms";
    };
    serviceConfig = {
      Type = "simple";
      ExecStartPre = "${pkgs.bash}/bin/bash -c '${pkgs.coreutils}/bin/mkdir -p /var/lib/firecracker/{rootfs,vms} && ${pkgs.iptables}/sbin/iptables -t nat -C POSTROUTING -s 172.16.0.0/24 -o eth0 -j MASQUERADE 2>/dev/null || ${pkgs.iptables}/sbin/iptables -t nat -A POSTROUTING -s 172.16.0.0/24 -o eth0 -j MASQUERADE && ${pkgs.iptables}/sbin/iptables -C FORWARD -i br-fc -o eth0 -j ACCEPT 2>/dev/null || ${pkgs.iptables}/sbin/iptables -A FORWARD -i br-fc -o eth0 -j ACCEPT'";
      ExecStart = "${pkgs.python3}/bin/python3 /var/lib/firecracker/fc-daemon.py";
      Restart = "on-failure";
      RestartSec = 5;
    };
  };

  system.stateVersion = "26.05";
}
