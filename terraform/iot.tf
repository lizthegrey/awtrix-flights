# IoT Core custom authorizer for MQTT username/password auth.
#
# The device connects with TLS to the account's ATS endpoint and encodes the
# authorizer name into its MQTT username via the magic query string
# "?x-amz-customauthorizer-name=<name>". IoT Core strips this before invoking
# the authorizer Lambda, which sees just the bare username.

resource "aws_iot_authorizer" "mqtt" {
  name                    = "${var.name_prefix}-mqtt-authorizer"
  authorizer_function_arn = aws_lambda_function.authorizer.arn
  signing_disabled        = true # username/password, no token signing
  status                  = "ACTIVE"
  enable_caching_for_http = false
}

# Optional IoT "thing" for console organization. No policy attached — auth
# comes via the custom authorizer + the IAM policy it returns.
resource "aws_iot_thing" "awtrix" {
  name = var.awtrix_client_id
}
