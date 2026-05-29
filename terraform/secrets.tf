# Random password generated once by terraform. Output via `terraform output -raw mqtt_password`
# so you can paste it into the AWTRIX device's MQTT settings.
resource "random_password" "mqtt" {
  length  = 32
  special = false # AWTRIX MQTT config field is fiddly with shell-special chars
}

resource "aws_secretsmanager_secret" "mqtt" {
  name        = "${var.name_prefix}/mqtt-creds"
  description = "Username/password the AWTRIX device uses to authenticate to IoT Core."
  tags        = local.default_tags

  # Allow `terraform destroy` then re-apply without the 7-day recovery window.
  recovery_window_in_days = 0
}

resource "aws_secretsmanager_secret_version" "mqtt" {
  secret_id = aws_secretsmanager_secret.mqtt.id
  secret_string = jsonencode({
    username = "awtrix"
    password = random_password.mqtt.result
  })
}
