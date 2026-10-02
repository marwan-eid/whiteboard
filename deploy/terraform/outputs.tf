output "public_ip" {
  value = oci_core_instance.demo.public_ip
}

output "url" {
  value = "https://${var.site_address != "" ? var.site_address : "${replace(oci_core_instance.demo.public_ip, ".", "-")}.sslip.io"}"
}

output "ssh" {
  value = "ssh ubuntu@${oci_core_instance.demo.public_ip}"
}

output "grafana_admin_password" {
  value     = random_password.grafana.result
  sensitive = true
}

output "backup_bucket" {
  value = oci_objectstorage_bucket.backups.name
}
