variable "region" {
  description = "AWS region for all resources."
  type        = string
  default     = "ap-southeast-2"
}

variable "name_prefix" {
  description = "Prefix applied to AWS resource names so multiple deploys can coexist."
  type        = string
  default     = "awtrix-flights"
}

variable "awtrix_client_id" {
  description = "MQTT client ID the AWTRIX device will present. Matches what you configure in the device's MQTT settings."
  type        = string
  default     = "awtrix_103"
}

variable "awtrix_topic" {
  description = "MQTT topic the device will subscribe to. AWTRIX renders the JSON payload as a custom app."
  type        = string
  # AWTRIX subscribes to <prefix>/custom/<appname>; matching prefix to client_id is convention.
  default = "awtrix_103/custom/overhead"
}

variable "schedule_expression" {
  description = "Schedule for the publisher Lambda. EventBridge Scheduler requires rate >= 1 minute; 1 minute is plenty given the ~2-minute lead time we project."
  type        = string
  default     = "rate(1 minute)"
}

variable "publisher_log_level" {
  description = "slog level for the publisher Lambda: debug | info | warn | error."
  type        = string
  default     = "info"
}

variable "awtrix_icon_id" {
  description = "Optional AWTRIX icon ID to display alongside the text. Empty for no icon."
  type        = string
  default     = ""
}

# Observer (home) location. Defaults to Ashfield, NSW 2131 centroid — fine for
# anyone in the suburb. Override in terraform.tfvars with your exact roof
# coordinates for tighter filtering (the .gitignore keeps tfvars out of git).
variable "home_lat" {
  description = "Observer latitude in decimal degrees."
  type        = number
  default     = -33.888
  sensitive   = true
}

variable "home_lon" {
  description = "Observer longitude in decimal degrees."
  type        = number
  default     = 151.125
  sensitive   = true
}

variable "tags" {
  description = "Extra tags applied to all resources."
  type        = map(string)
  default     = {}
}

variable "honeycomb_api_key" {
  description = "Honeycomb ingest API key. Empty disables OTel export (the Lambdas still run; spans become no-ops)."
  type        = string
  default     = ""
  sensitive   = true
}
