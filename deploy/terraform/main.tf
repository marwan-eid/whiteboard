# The public demo on Oracle Cloud Always Free (ADR-0006): one A1 VM running
# deploy/compose.prod.yaml, a bucket for nightly backups, and a network that
# admits SSH, HTTP and HTTPS. Everything lives in its own compartment, so
# nothing here can touch other projects in the account (MilkRun).

resource "oci_identity_compartment" "whiteboard" {
  compartment_id = var.tenancy_ocid
  name           = "whiteboard"
  description    = "Whiteboard demo (managed by Terraform, deploy/terraform)"
  enable_delete  = true
}

locals {
  compartment = oci_identity_compartment.whiteboard.id
}

data "oci_identity_availability_domains" "ads" {
  compartment_id = var.tenancy_ocid
}

# --- network -----------------------------------------------------------------

resource "oci_core_vcn" "main" {
  compartment_id = local.compartment
  display_name   = "whiteboard"
  cidr_blocks    = ["10.20.0.0/16"]
  dns_label      = "whiteboard"
}

resource "oci_core_internet_gateway" "main" {
  compartment_id = local.compartment
  vcn_id         = oci_core_vcn.main.id
  display_name   = "whiteboard"
}

resource "oci_core_route_table" "public" {
  compartment_id = local.compartment
  vcn_id         = oci_core_vcn.main.id
  display_name   = "public"
  route_rules {
    destination       = "0.0.0.0/0"
    network_entity_id = oci_core_internet_gateway.main.id
  }
}

resource "oci_core_security_list" "public" {
  compartment_id = local.compartment
  vcn_id         = oci_core_vcn.main.id
  display_name   = "public"

  egress_security_rules {
    destination = "0.0.0.0/0"
    protocol    = "all"
  }
  ingress_security_rules {
    source   = var.admin_cidr
    protocol = "6" # TCP
    tcp_options {
      min = 22
      max = 22
    }
  }
  dynamic "ingress_security_rules" {
    for_each = [80, 443]
    content {
      source   = "0.0.0.0/0"
      protocol = "6"
      tcp_options {
        min = ingress_security_rules.value
        max = ingress_security_rules.value
      }
    }
  }
  ingress_security_rules { # HTTP/3
    source   = "0.0.0.0/0"
    protocol = "17" # UDP
    udp_options {
      min = 443
      max = 443
    }
  }
}

resource "oci_core_subnet" "public" {
  compartment_id    = local.compartment
  vcn_id            = oci_core_vcn.main.id
  display_name      = "public"
  cidr_block        = "10.20.1.0/24"
  dns_label         = "public"
  route_table_id    = oci_core_route_table.public.id
  security_list_ids = [oci_core_security_list.public.id]
}

# --- the VM ------------------------------------------------------------------

data "oci_core_images" "ubuntu" {
  compartment_id           = var.tenancy_ocid
  operating_system         = "Canonical Ubuntu"
  operating_system_version = "24.04"
  shape                    = "VM.Standard.A1.Flex"
  sort_by                  = "TIMECREATED"
  sort_order               = "DESC"
}

resource "random_password" "secret" {
  length  = 48
  special = false
}
resource "random_password" "postgres" {
  length  = 32
  special = false
}
resource "random_password" "grafana" {
  length  = 24
  special = false
}

resource "oci_core_instance" "demo" {
  compartment_id      = local.compartment
  availability_domain = data.oci_identity_availability_domains.ads.availability_domains[0].name
  display_name        = "whiteboard"
  shape               = "VM.Standard.A1.Flex"
  shape_config {
    ocpus         = var.ocpus
    memory_in_gbs = var.memory_gb
  }
  source_details {
    source_type             = "image"
    source_id               = data.oci_core_images.ubuntu.images[0].id
    boot_volume_size_in_gbs = var.boot_volume_gb
  }
  create_vnic_details {
    subnet_id        = oci_core_subnet.public.id
    assign_public_ip = true
  }
  metadata = {
    ssh_authorized_keys = file(var.ssh_public_key_path)
    user_data = base64encode(templatefile("${path.module}/cloud-init.sh", {
      repo_url          = var.repo_url
      image_tag         = var.image_tag
      site_address      = var.site_address
      duckdns_token     = var.duckdns_token
      secret            = random_password.secret.result
      postgres_password = random_password.postgres.result
      grafana_password  = random_password.grafana.result
      backup_url        = local.backup_upload_url
    }))
  }
  # A newer image or an edited setup script must not silently replace the demo.
  lifecycle {
    ignore_changes = [source_details[0].source_id, metadata]
  }
}

# --- backups -----------------------------------------------------------------

data "oci_objectstorage_namespace" "ns" {
  compartment_id = var.tenancy_ocid
}

resource "oci_objectstorage_bucket" "backups" {
  compartment_id = local.compartment
  namespace      = data.oci_objectstorage_namespace.ns.namespace
  name           = "whiteboard-backups"
  access_type    = "NoPublicAccess"
}

# Object Storage deletes old backups itself, and needs permission to.
resource "oci_identity_policy" "lifecycle" {
  compartment_id = var.tenancy_ocid
  name           = "whiteboard-backup-lifecycle"
  description    = "Lets Object Storage expire old whiteboard backups"
  statements = [
    "Allow service objectstorage-${var.region} to manage object-family in compartment id ${local.compartment}",
  ]
}

resource "oci_objectstorage_object_lifecycle_policy" "backups" {
  namespace = data.oci_objectstorage_namespace.ns.namespace
  bucket    = oci_objectstorage_bucket.backups.name
  rules {
    name        = "expire-old-backups"
    action      = "DELETE"
    is_enabled  = true
    time_amount = var.backup_retention_days
    time_unit   = "DAYS"
    target      = "objects"
  }
  depends_on = [oci_identity_policy.lifecycle]
}

# The VM uploads through a write-only pre-authenticated URL: it holds no
# account credentials, and the URL cannot read or list backups.
resource "oci_objectstorage_preauthrequest" "upload" {
  namespace    = data.oci_objectstorage_namespace.ns.namespace
  bucket       = oci_objectstorage_bucket.backups.name
  name         = "whiteboard-backup-upload"
  access_type  = "AnyObjectWrite"
  time_expires = timeadd(plantimestamp(), "17520h") # two years
  lifecycle {
    ignore_changes = [time_expires]
  }
}

locals {
  backup_upload_url = "https://objectstorage.${var.region}.oraclecloud.com${oci_objectstorage_preauthrequest.upload.access_uri}"
}
