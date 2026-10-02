# Account access: an API signing key for your OCI user (docs/DEPLOY.md, step 1).
variable "tenancy_ocid" { type = string }
variable "user_ocid" { type = string }
variable "fingerprint" { type = string }
variable "private_key_path" { type = string }
variable "region" {
  type        = string
  description = "Your home region, e.g. eu-frankfurt-1: Always Free A1 capacity is only in the home region."
}

variable "ssh_public_key_path" {
  type        = string
  description = "Public key allowed to SSH in as ubuntu."
}
variable "admin_cidr" {
  type        = string
  description = "Who may reach SSH (port 22); your address as a.b.c.d/32 is safest."
  default     = "0.0.0.0/0"
}

# Size of the VM, within the Always Free A1 allowance. The demo account's
# allowance is 2 OCPU / 12 GB (see ADR-0006); some accounts get 4 / 24.
variable "ocpus" {
  type    = number
  default = 2
}
variable "memory_gb" {
  type    = number
  default = 12
}
variable "boot_volume_gb" {
  type        = number
  description = "Of the 200 GB of free block storage per account."
  default     = 100
}

variable "site_address" {
  type        = string
  description = "Domain for the demo. Empty means <ip with dashes>.sslip.io, which needs no account anywhere."
  default     = ""
}
variable "duckdns_token" {
  type        = string
  description = "If site_address is a duckdns.org name: its token, so the VM keeps the record pointed at itself."
  default     = ""
  sensitive   = true
}
variable "repo_url" {
  type    = string
  default = "https://github.com/marwan-eid/whiteboard.git"
}
variable "image_tag" {
  type    = string
  default = "latest"
}
variable "backup_retention_days" {
  type    = number
  default = 14
}
