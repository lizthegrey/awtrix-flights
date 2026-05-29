# Compile the Lambda binaries. We track the source file hashes as a trigger so
# the binaries rebuild any time Go code changes, then archive them as zips for
# Lambda's `provided.al2023` runtime (binary must be named "bootstrap").

locals {
  go_sources = setunion(
    fileset("${path.module}/..", "go.{mod,sum}"),
    fileset("${path.module}/..", "cmd/**/*.go"),
    fileset("${path.module}/..", "internal/**/*.go"),
  )
  source_hash = sha256(join("", [for f in local.go_sources : filesha256("${path.module}/../${f}")]))
}

resource "null_resource" "build_publisher" {
  triggers = { source_hash = local.source_hash }
  provisioner "local-exec" {
    working_dir = "${path.module}/.."
    interpreter = ["/bin/bash", "-c"]
    command     = <<-EOT
      set -euo pipefail
      mkdir -p terraform/build/publisher
      GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' \
          -o terraform/build/publisher/bootstrap ./cmd/publisher
    EOT
  }
}

resource "null_resource" "build_authorizer" {
  triggers = { source_hash = local.source_hash }
  provisioner "local-exec" {
    working_dir = "${path.module}/.."
    interpreter = ["/bin/bash", "-c"]
    command     = <<-EOT
      set -euo pipefail
      mkdir -p terraform/build/authorizer
      GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' \
          -o terraform/build/authorizer/bootstrap ./cmd/authorizer
    EOT
  }
}

data "archive_file" "publisher" {
  depends_on  = [null_resource.build_publisher]
  type        = "zip"
  source_file = "${path.module}/build/publisher/bootstrap"
  output_path = "${path.module}/build/publisher.zip"
}

data "archive_file" "authorizer" {
  depends_on  = [null_resource.build_authorizer]
  type        = "zip"
  source_file = "${path.module}/build/authorizer/bootstrap"
  output_path = "${path.module}/build/authorizer.zip"
}
