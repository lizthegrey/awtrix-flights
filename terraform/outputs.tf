output "mqtt_broker_host" {
  description = "AWS IoT Core ATS endpoint. Use this as the MQTT broker host on the AWTRIX device."
  value       = data.aws_iot_endpoint.ats.endpoint_address
}

output "mqtt_broker_port" {
  description = "MQTT port. IoT Core only accepts TLS on 8883 (or MQTT-over-WSS on 443)."
  value       = 8883
}

output "mqtt_client_id" {
  description = "MQTT client ID the device should present. Configure this on the AWTRIX."
  value       = var.awtrix_client_id
}

output "mqtt_username" {
  description = <<-EOT
    MQTT username to configure on the AWTRIX. The ?x-amz-customauthorizer-name
    suffix tells IoT Core which custom authorizer to invoke; IoT Core strips
    it before passing the bare username to the authorizer Lambda.
  EOT
  value       = "awtrix?x-amz-customauthorizer-name=${aws_iot_authorizer.mqtt.name}"
}

output "mqtt_password" {
  description = "MQTT password to configure on the AWTRIX. Retrieve with: terraform output -raw mqtt_password"
  value       = random_password.mqtt.result
  sensitive   = true
}

output "mqtt_topic" {
  description = "Topic to subscribe to in the AWTRIX MQTT settings."
  value       = var.awtrix_topic
}

output "publisher_log_group" {
  description = "CloudWatch log group for the publisher Lambda (tail with: aws logs tail --follow ...)."
  value       = aws_cloudwatch_log_group.publisher.name
}

output "authorizer_log_group" {
  description = "CloudWatch log group for the authorizer Lambda."
  value       = aws_cloudwatch_log_group.authorizer.name
}
