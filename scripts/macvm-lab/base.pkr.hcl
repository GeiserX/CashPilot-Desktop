# Lab only: build the Secure VM base image from Apple's own IPSW.
#
# The boot_command drives macOS Setup Assistant over Virtualization.framework's
# VNC server (tart run --vnc-experimental), the same way the MIT-licensed
# cirruslabs/macos-image-templates vanilla-tahoe template does. Differences:
#   - the admin account name and password come in as variables, never "admin";
#   - Screen Sharing and Gatekeeper are left alone (Gatekeeper stays on);
#   - Remote Login is on only for the build. provision.sh turns it off again.
#
# The build then installs the guest agent (provision.sh base) and turns Remote
# Login off from the next boot on, so a clone of this image is reached only
# through `tart exec`.
#
# Usage (the password is random and never written to this file):
#   PKR_VAR_admin_password="$(openssl rand -hex 12)" \
#   packer build -var ipsw=/path/UniversalMac_26.6.2_25G83_Restore.ipsw \
#     -var vm_name=cp-base -var agent=/path/tart-guest-agent \
#     -var agent_sha256=<sha256 from the release checksums> base.pkr.hcl
packer {
  required_plugins {
    tart = {
      version = ">= 1.16.0"
      source  = "github.com/cirruslabs/tart"
    }
  }
}

variable "ipsw" { type = string }
variable "vm_name" { type = string }
variable "admin_user" {
  type    = string
  default = "cpadmin"
}
# Hex only: it is quoted into one shell command below, and it shows in the build
# guest's process list while that command runs. Each clone rotates it.
variable "admin_password" {
  type      = string
  sensitive = true
  validation {
    condition     = can(regex("^[0-9a-f]{16,}$", var.admin_password))
    error_message = "admin_password must be at least 16 lowercase hex digits (openssl rand -hex 12)."
  }
}
# The tart-guest-agent binary (darwin, from its GitHub release) and its SHA-256.
variable "agent" { type = string }
variable "agent_sha256" { type = string }
variable "cpu_count" {
  type    = number
  default = 4
}
variable "memory_gb" {
  type    = number
  default = 6
}
variable "disk_size_gb" {
  type    = number
  default = 80
}
# Seconds to wait before the first keystroke. A slow host needs more.
variable "hello_wait" {
  type    = string
  default = "60s"
}

source "tart-cli" "base" {
  from_ipsw    = var.ipsw
  vm_name      = var.vm_name
  cpu_count    = var.cpu_count
  memory_gb    = var.memory_gb
  disk_size_gb = var.disk_size_gb
  ssh_username = var.admin_user
  ssh_password = var.admin_password
  ssh_timeout  = "300s"
  boot_command = [
    "<wait${var.hello_wait}><spacebar>",
    # Language: switch away and back so "english" lands on English, not English (UK).
    "<wait30s>italiano<esc>english<enter>",
    "<wait60s><click 'Select Your Country or Region'><wait5s>united states<leftShiftOn><tab><leftShiftOff><spacebar>",
    # Transfer Your Data to This Mac: not now
    "<wait10s><tab><tab><tab><spacebar><tab><tab><spacebar>",
    # Written and Spoken Languages, Accessibility, Data & Privacy
    "<wait10s><leftShiftOn><tab><leftShiftOff><spacebar>",
    "<wait10s><leftShiftOn><tab><leftShiftOff><spacebar>",
    "<wait10s><leftShiftOn><tab><leftShiftOff><spacebar>",
    # Create a Mac Account: full name, account name, password, verify
    "<wait10s><tab><tab><tab><tab><tab><tab>CashPilot Admin<tab>${var.admin_user}<tab>${var.admin_password}<tab>${var.admin_password}<tab><tab><spacebar><tab><tab><spacebar>",
    # VoiceOver on, so the Apple Account screen takes keyboard focus
    "<wait120s><leftAltOn><f5><leftAltOff>",
    # Sign In with Your Apple Account: skip, then confirm
    "<wait10s><leftShiftOn><tab><leftShiftOff><spacebar><up><spacebar>",
    "<wait10s><tab><spacebar>",
    # Terms and Conditions: agree, confirm
    "<wait10s><leftShiftOn><tab><leftShiftOff><spacebar>",
    "<wait10s><tab><spacebar>",
    # Age Range: adult
    "<wait10s><tab><tab><tab><spacebar>",
    # Location Services: off, confirm
    "<wait10s><leftShiftOn><tab><leftShiftOff><spacebar>",
    "<wait10s><tab><spacebar>",
    # Time Zone: UTC
    "<wait10s><tab><tab><tab>UTC<enter><leftShiftOn><tab><leftShiftOff><spacebar>",
    # Analytics: off
    "<wait10s><leftShiftOn><tab><leftShiftOff><spacebar>",
    # Screen Time: later
    "<wait10s><tab><tab><spacebar>",
    # Siri: off
    "<wait10s><tab><spacebar><leftShiftOn><tab><leftShiftOff><spacebar>",
    # FileVault: not now, confirm (an unattended guest cannot unlock FileVault)
    "<wait10s><leftShiftOn><tab><tab><leftShiftOff><spacebar>",
    "<wait10s><tab><spacebar>",
    # Choose Your Look
    "<wait10s><leftShiftOn><tab><leftShiftOff><spacebar>",
    # Update Mac Automatically: continue with the defaults
    "<wait10s><tab><tab><spacebar>",
    # Welcome to Mac
    "<wait30s><spacebar>",
    # VoiceOver off
    "<wait10s><leftAltOn><f5><leftAltOff>",
    # Keyboard navigation, so System Settings can be driven by Tab
    "<wait10s><leftAltOn><spacebar><leftAltOff>Terminal<wait10s><enter>",
    "<wait20s>defaults write NSGlobalDomain AppleKeyboardUIMode -int 3<enter>",
    "<wait10s>open '/System/Applications/System Settings.app'<enter>",
    "<wait120s>",
    # General > Sharing. The template enables Screen Sharing first (the first
    # toggle, which also asks for the password); the tab count to Remote Login
    # depends on it, so we keep the same order. provision.sh turns both off.
    "<wait10s><leftCtrlOn><f2><leftCtrlOff><right><right><right><down>Sharing<enter>",
    "<wait10s><tab><tab><tab><tab><tab><spacebar>",
    "<wait10s>${var.admin_password}<enter>",
    "<wait10s><tab><tab><tab><tab><tab><tab><tab><tab><tab><tab><tab><tab><spacebar>",
    "<wait10s><leftAltOn>q<leftAltOff>",
  ]
  create_grace_time  = "30s"
  recovery_partition = "keep"
}

build {
  sources = ["source.tart-cli.base"]

  # Prove we got in, and record what we built from.
  provisioner "shell" {
    inline = [
      "sw_vers",
      "id",
      "spctl --status",
      "csrutil status",
    ]
  }

  provisioner "file" {
    source      = var.agent
    destination = "/tmp/tart-guest-agent"
  }
  provisioner "file" {
    source      = "${path.root}/provision.sh"
    destination = "/tmp/provision.sh"
  }
  provisioner "shell" {
    execute_command = "chmod +x {{ .Path }}; printf '%s\\n' '${var.admin_password}' | sudo -S -p '' sh -c '{{ .Vars }} {{ .Path }}'"
    inline = [
      "/bin/sh /tmp/provision.sh base /tmp/tart-guest-agent ${var.agent_sha256}",
      "rm -f /tmp/tart-guest-agent /tmp/provision.sh",
    ]
  }
}
