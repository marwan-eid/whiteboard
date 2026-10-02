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

output "fallback_url" {
  value = var.fallback_micro ? "https://${replace(oci_core_instance.fallback[0].public_ip, ".", "-")}.sslip.io" : null
}

output "fallback_ssh" {
  value = var.fallback_micro ? "ssh ubuntu@${oci_core_instance.fallback[0].public_ip}" : null
}
