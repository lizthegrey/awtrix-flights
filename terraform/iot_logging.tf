# IoT Core v2 logging — writes connect/disconnect/auth/permission failures
# to CloudWatch so we can diagnose device-side connectivity. Off by default
# (level NONE) so we don't pay for chatter once everything is healthy.

variable "iot_log_level" {
  description = "Default IoT Core v2 log level. Use INFO/DEBUG while debugging, NONE in steady state. WARN catches denied connections."
  type        = string
  default     = "WARN"
  validation {
    condition     = contains(["DEBUG", "INFO", "ERROR", "WARN", "DISABLED"], var.iot_log_level)
    error_message = "iot_log_level must be one of DEBUG, INFO, ERROR, WARN, DISABLED."
  }
}

resource "aws_iam_role" "iot_logging" {
  name = "${var.name_prefix}-iot-logging"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "iot.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
  tags = local.default_tags
}

resource "aws_iam_role_policy_attachment" "iot_logging" {
  role       = aws_iam_role.iot_logging.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSIoTLogging"
}

resource "aws_iot_logging_options" "main" {
  role_arn         = aws_iam_role.iot_logging.arn
  default_log_level = var.iot_log_level
  disable_all_logs = false

  depends_on = [aws_iam_role_policy_attachment.iot_logging]
}
