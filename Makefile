.PHONY: test vet fmt build plan apply destroy clean

test:
	go test ./...

vet:
	go vet ./...
	gofmt -l cmd internal | tee /dev/stderr | (! grep .)

fmt:
	gofmt -w cmd internal
	cd terraform && terraform fmt

build:
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' \
	    -o terraform/build/publisher/bootstrap ./cmd/publisher
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' \
	    -o terraform/build/authorizer/bootstrap ./cmd/authorizer

plan: build
	cd terraform && terraform plan -refresh=false

apply: build
	cd terraform && terraform apply

destroy:
	cd terraform && terraform destroy

clean:
	rm -rf terraform/build
