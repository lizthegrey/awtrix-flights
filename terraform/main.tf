data "aws_caller_identity" "current" {}
data "aws_region" "current" {}

# IoT data-plane endpoint for the publisher Lambda to call Publish against.
data "aws_iot_endpoint" "ats" {
  endpoint_type = "iot:Data-ATS"
}

locals {
  account_id = data.aws_caller_identity.current.account_id
  region     = data.aws_region.current.name

  default_tags = merge({
    Project   = "awtrix-flights"
    ManagedBy = "terraform"
  }, var.tags)
}
