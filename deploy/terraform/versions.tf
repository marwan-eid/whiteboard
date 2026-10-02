terraform {
  required_version = ">= 1.6"
  required_providers {
    oci    = { source = "oracle/oci", version = "~> 9.8" }
    random = { source = "hashicorp/random", version = "~> 3.9" }
  }
}

provider "oci" {
  tenancy_ocid     = var.tenancy_ocid
  user_ocid        = var.user_ocid
  fingerprint      = var.fingerprint
  private_key_path = var.private_key_path
  region           = var.region
}
